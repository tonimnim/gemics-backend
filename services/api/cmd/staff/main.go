// gamics-staff manages platform staff accounts from the command line. It
// exists for the first admin; after that, admins grant roles in the dashboard.
//
//	gamics-staff create <konami-id> <username> <support|reviewer|operator|admin>
//	gamics-staff grant  <konami-id> <support|reviewer|operator|admin>
//
// Players sign up with Google or Apple, but staff sign in to the dashboard
// with a Konami ID and password, and production has no password sign-up. So
// create makes the account: it reads the password from standard input, one
// line, so it never appears in the shell history or the process list.
package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"regexp"
	"slices"
	"strings"
	"time"

	gamicsauth "github.com/gamics-io/gamics/services/api/internal/auth"
	"github.com/gamics-io/gamics/services/api/internal/database"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

var (
	roles           = []string{"support", "reviewer", "operator", "admin"}
	usernamePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.]{2,23}$`)
	konamiPattern   = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9 _.-]{5,63}$`)
)

func usage() {
	fmt.Fprintln(os.Stderr, "usage: gamics-staff create <konami-id> <username> <support|reviewer|operator|admin>  (password on stdin)")
	fmt.Fprintln(os.Stderr, "       gamics-staff grant <konami-id> <support|reviewer|operator|admin>")
	os.Exit(2)
}

func main() {
	if len(os.Args) < 2 {
		usage()
	}
	switch {
	case os.Args[1] == "grant" && len(os.Args) == 4 && slices.Contains(roles, os.Args[3]):
		run(func(ctx context.Context, db *database.Cluster) error { return grant(ctx, db, os.Args[2], os.Args[3]) })
	case os.Args[1] == "create" && len(os.Args) == 5 && slices.Contains(roles, os.Args[4]):
		run(func(ctx context.Context, db *database.Cluster) error {
			return create(ctx, db, os.Args[2], os.Args[3], os.Args[4])
		})
	default:
		usage()
	}
}

func run(command func(context.Context, *database.Cluster) error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	writer := os.Getenv("DATABASE_WRITE_URL")
	db, err := database.Open(ctx, writer, writer, 1, 1)
	if err != nil {
		fmt.Fprintln(os.Stderr, "database configuration failed")
		os.Exit(1)
	}
	defer db.Close()
	if err = command(ctx, db); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

// grant gives an existing account a staff role. Konami IDs compare on their
// uppercase alphanumeric form, exactly as the unique index and sign-in do.
func grant(ctx context.Context, db *database.Cluster, konamiID, role string) error {
	var userID, username string
	err := db.Writer.QueryRow(ctx, `SELECT player.id::text,COALESCE(profile.handle,'')
		FROM game_accounts account JOIN users player ON player.id=account.user_id
		LEFT JOIN player_profiles profile ON profile.user_id=player.id
		WHERE account.game_id='efootball-mobile' AND account.publisher_player_id IS NOT NULL
		AND upper(regexp_replace(account.publisher_player_id,'[^A-Za-z0-9]+','','g'))=
		    upper(regexp_replace($1,'[^A-Za-z0-9]+','','g'))
		AND player.status='active'`, konamiID).Scan(&userID, &username)
	if errors.Is(err, pgx.ErrNoRows) {
		return errors.New("no active account has that Konami ID; create it with gamics-staff create")
	}
	if err != nil {
		return fmt.Errorf("account lookup failed: %w", err)
	}
	tx, err := db.Writer.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	if err = grantRole(ctx, tx, userID, role); err != nil {
		return fmt.Errorf("grant failed: %w", err)
	}
	if err = tx.Commit(ctx); err != nil {
		return err
	}
	fmt.Printf("%s is now %s\n", username, role)
	return nil
}

// create makes a staff account with a password, in one transaction.
func create(ctx context.Context, db *database.Cluster, konamiID, username, role string) error {
	konamiID = strings.TrimSpace(konamiID)
	if !konamiPattern.MatchString(konamiID) {
		return errors.New("the Konami ID must be 6-64 letters, digits, spaces, dots, dashes or underscores")
	}
	if !usernamePattern.MatchString(username) {
		return errors.New("the username must be 3-24 letters, numbers, underscores or dots")
	}
	fmt.Fprint(os.Stderr, "password: ")
	line, err := bufio.NewReader(os.Stdin).ReadString('\n')
	if err != nil && line == "" {
		return errors.New("no password on standard input")
	}
	password := strings.TrimRight(line, "\r\n")
	if !gamicsauth.ValidPasswordLength(password) {
		return errors.New("the password must be 8-128 characters")
	}
	hash, err := gamicsauth.HashPassword(password)
	if err != nil {
		return err
	}
	tx, err := db.Writer.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	var userID string
	if err = tx.QueryRow(ctx, `INSERT INTO users(display_name,password_hash,password_changed_at,status,onboarded_at)
		VALUES ($1,$2,now(),'active',now()) RETURNING id::text`, username, hash).Scan(&userID); err != nil {
		return fmt.Errorf("create failed: %w", err)
	}
	if _, err = tx.Exec(ctx, `INSERT INTO player_profiles(user_id,handle) VALUES ($1,$2)`, userID, username); err != nil {
		return explainConflict(err, "player_profiles_handle_unique", "that username is taken")
	}
	if _, err = tx.Exec(ctx, `INSERT INTO game_accounts(user_id,game_id,in_game_name,publisher_player_id)
		VALUES ($1,'efootball-mobile','',$2)`, userID, konamiID); err != nil {
		return explainConflict(err, "game_accounts_publisher_id_unique", "that Konami ID already has an account; use gamics-staff grant")
	}
	if err = grantRole(ctx, tx, userID, role); err != nil {
		return fmt.Errorf("grant failed: %w", err)
	}
	if err = tx.Commit(ctx); err != nil {
		return err
	}
	fmt.Printf("created %s as %s; sign in to the dashboard with the Konami ID and that password\n", username, role)
	return nil
}

func grantRole(ctx context.Context, tx pgx.Tx, userID, role string) error {
	if _, err := tx.Exec(ctx, `INSERT INTO platform_staff_roles(user_id,role) VALUES ($1,$2)
		ON CONFLICT (user_id) DO UPDATE SET role=EXCLUDED.role,granted_at=now(),revoked_at=NULL`, userID, role); err != nil {
		return err
	}
	_, err := tx.Exec(ctx, `INSERT INTO audit_events(action,subject_type,subject_id,after_state)
		VALUES ('staff.role_granted','user',$1,jsonb_build_object('role',$2::text,'via','gamics-staff'))`, userID, role)
	return err
}

func explainConflict(err error, constraint, message string) error {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" && pgErr.ConstraintName == constraint {
		return errors.New(message)
	}
	return fmt.Errorf("create failed: %w", err)
}
