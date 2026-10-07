// gamics-staff grants a platform staff role from the command line. It exists
// to create the first admin; after that, admins grant roles in the dashboard.
//
//	gamics-staff grant <konami-id> <support|reviewer|operator|admin>
package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"slices"
	"time"

	"github.com/gamics-io/gamics/services/api/internal/database"
	"github.com/jackc/pgx/v5"
)

var roles = []string{"support", "reviewer", "operator", "admin"}

func main() {
	if len(os.Args) != 4 || os.Args[1] != "grant" || !slices.Contains(roles, os.Args[3]) {
		fmt.Fprintln(os.Stderr, "usage: gamics-staff grant <konami-id> <support|reviewer|operator|admin>")
		os.Exit(2)
	}
	konamiID, role := os.Args[2], os.Args[3]
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	writer := os.Getenv("DATABASE_WRITE_URL")
	db, err := database.Open(ctx, writer, writer, 1, 1)
	if err != nil {
		fmt.Fprintln(os.Stderr, "database configuration failed")
		os.Exit(1)
	}
	defer db.Close()
	// Konami IDs compare on their uppercase alphanumeric form, exactly as the
	// unique index and the sign-in lookup do.
	var userID, username string
	err = db.Writer.QueryRow(ctx, `SELECT player.id::text,COALESCE(profile.handle,'')
		FROM game_accounts account JOIN users player ON player.id=account.user_id
		LEFT JOIN player_profiles profile ON profile.user_id=player.id
		WHERE account.game_id='efootball-mobile' AND account.publisher_player_id IS NOT NULL
		AND upper(regexp_replace(account.publisher_player_id,'[^A-Za-z0-9]+','','g'))=
		    upper(regexp_replace($1,'[^A-Za-z0-9]+','','g'))
		AND player.status='active'`, konamiID).Scan(&userID, &username)
	if errors.Is(err, pgx.ErrNoRows) {
		fmt.Fprintln(os.Stderr, "no active player has that Konami ID; register the account first")
		os.Exit(1)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "player lookup failed:", err)
		os.Exit(1)
	}
	if _, err = db.Writer.Exec(ctx, `INSERT INTO platform_staff_roles(user_id,role) VALUES ($1,$2)
		ON CONFLICT (user_id) DO UPDATE SET role=EXCLUDED.role,granted_at=now(),revoked_at=NULL`, userID, role); err == nil {
		_, err = db.Writer.Exec(ctx, `INSERT INTO audit_events(action,subject_type,subject_id,after_state)
			VALUES ('staff.role_granted','user',$1,jsonb_build_object('role',$2::text,'via','gamics-staff'))`, userID, role)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "grant failed:", err)
		os.Exit(1)
	}
	fmt.Printf("%s is now %s\n", username, role)
}
