import { Feather } from '@expo/vector-icons';
import * as ImagePicker from 'expo-image-picker';
import { router, useLocalSearchParams } from 'expo-router';
import { useMemo, useState } from 'react';
import {
  Image,
  KeyboardAvoidingView,
  Platform,
  Pressable,
  ScrollView,
  StyleSheet,
  Text,
  TextInput,
  View,
} from 'react-native';
import { SafeAreaView } from 'react-native-safe-area-context';

import { colors, fonts, radius, spacing } from '@/design/tokens';
import { getDemoMatchRoom } from '@/features/matches/demo';
import type {
  LocalEvidenceAsset,
  MatchAllowedAction,
  MatchLifecycle,
  MatchParticipant,
  ResultVerification,
  ScoreReport,
  ScoreTiebreak,
} from '@/features/matches/types';

const MAX_SCREENSHOT_BYTES = 10 * 1024 * 1024;
const MAX_FINAL_SCREENSHOTS = 3;
const SCREENSHOT_MEDIA_TYPES = ['image/jpeg', 'image/png'];
/** The demo mirrors the server's default report and response windows. */
const DEMO_WINDOW_MS = 10 * 60 * 1000;

type FeatherIcon = keyof typeof Feather.glyphMap;
type ScoreClaim = Pick<ScoreReport, 'homeScore' | 'awayScore' | 'tiebreak'>;
type ScoreDraft = { home: string; away: string; homePenalties: string; awayPenalties: string };

const emptyDraft: ScoreDraft = { home: '', away: '', homePenalties: '', awayPenalties: '' };

function digitsOnly(value: string) {
  return value.replace(/\D/g, '');
}

function formatDeadline(deadline: string | null) {
  if (!deadline) return 'the deadline';
  return new Date(deadline).toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' });
}

function localReport(kind: ScoreReport['kind'], claim: ScoreClaim): ScoreReport {
  return {
    id: `local-${kind}-report`,
    kind,
    ...claim,
    games: [{ homeScore: claim.homeScore, awayScore: claim.awayScore }],
    reportedAt: new Date().toISOString(),
  };
}

function Avatar({ player, accent = colors.blue }: { player: MatchParticipant; accent?: string }) {
  return (
    <View style={[styles.avatar, { backgroundColor: accent }]}>
      <Text style={styles.avatarText}>{player.initials}</Text>
    </View>
  );
}

function Instruction({ number, title, detail, complete = false }: { number: number; title: string; detail: string; complete?: boolean }) {
  return (
    <View style={styles.instruction}>
      <View style={[styles.instructionNumber, complete && styles.instructionComplete]}>
        {complete ? <Feather color={colors.ink} name="check" size={14} /> : <Text style={styles.instructionNumberText}>{number}</Text>}
      </View>
      <View style={styles.instructionCopy}>
        <Text style={styles.instructionTitle}>{title}</Text>
        <Text style={styles.instructionDetail}>{detail}</Text>
      </View>
    </View>
  );
}

function ScoreInput({ label, value, onChangeText }: { label: string; value: string; onChangeText: (next: string) => void }) {
  return (
    <View style={styles.scoreSide}>
      <Text numberOfLines={1} style={styles.scoreLabel}>{label}</Text>
      <TextInput
        accessibilityLabel={`${label} score`}
        inputMode="numeric"
        keyboardType="number-pad"
        maxLength={2}
        onChangeText={(next) => onChangeText(digitsOnly(next))}
        placeholder="0"
        placeholderTextColor={colors.subtleInk}
        selectTextOnFocus
        style={styles.scoreInput}
        value={value}
      />
    </View>
  );
}

function ScoreFields({ draft, onChange, homeLabel, awayLabel, penaltiesRequired }: {
  draft: ScoreDraft;
  onChange: (next: ScoreDraft) => void;
  homeLabel: string;
  awayLabel: string;
  penaltiesRequired: boolean;
}) {
  return (
    <>
      <Text style={styles.inputLabel}>FINAL SCORE</Text>
      <View style={styles.scoreEntry}>
        <ScoreInput label={homeLabel} onChangeText={(home) => onChange({ ...draft, home })} value={draft.home} />
        <Text style={styles.scoreSeparator}>–</Text>
        <ScoreInput label={awayLabel} onChangeText={(away) => onChange({ ...draft, away })} value={draft.away} />
      </View>
      {penaltiesRequired ? (
        <View style={styles.penaltiesBlock}>
          <Text style={styles.inputLabel}>PENALTY SHOOTOUT</Text>
          <View style={styles.penaltyRow}>
            <TextInput accessibilityLabel={`${homeLabel} penalties`} inputMode="numeric" keyboardType="number-pad" maxLength={2} onChangeText={(next) => onChange({ ...draft, homePenalties: digitsOnly(next) })} placeholder="0" placeholderTextColor={colors.subtleInk} style={styles.penaltyInput} value={draft.homePenalties} />
            <Text style={styles.penaltySeparator}>–</Text>
            <TextInput accessibilityLabel={`${awayLabel} penalties`} inputMode="numeric" keyboardType="number-pad" maxLength={2} onChangeText={(next) => onChange({ ...draft, awayPenalties: digitsOnly(next) })} placeholder="0" placeholderTextColor={colors.subtleInk} style={styles.penaltyInput} value={draft.awayPenalties} />
          </View>
        </View>
      ) : null}
    </>
  );
}

function Declaration({ checked, onToggle, text }: { checked: boolean; onToggle: () => void; text: string }) {
  return (
    <Pressable accessibilityRole="checkbox" accessibilityState={{ checked }} onPress={onToggle} style={styles.declaration}>
      <View style={[styles.checkbox, checked && styles.checkboxChecked]}>{checked ? <Feather color={colors.ink} name="check" size={14} /> : null}</View>
      <Text style={styles.declarationText}>{text}</Text>
    </Pressable>
  );
}

/** Shows one of the viewer's own reports. The opponent's claim is never available. */
function OwnScore({ label, report, homeHandle, awayHandle }: { label: string; report: ScoreClaim; homeHandle: string; awayHandle: string }) {
  return (
    <View style={styles.ownScore}>
      <Text style={styles.inputLabel}>{label}</Text>
      <View style={styles.readOnlyScore}>
        <View style={styles.readOnlyPlayer}><Text numberOfLines={1} style={styles.readOnlyHandle}>{homeHandle}</Text><Text style={styles.readOnlyNumber}>{report.homeScore}</Text></View>
        <Text style={styles.scoreDash}>–</Text>
        <View style={styles.readOnlyPlayer}><Text numberOfLines={1} style={styles.readOnlyHandle}>{awayHandle}</Text><Text style={styles.readOnlyNumber}>{report.awayScore}</Text></View>
      </View>
      {report.tiebreak ? <Text style={styles.tiebreakNote}>Penalties {report.tiebreak.homeScore}–{report.tiebreak.awayScore}</Text> : null}
    </View>
  );
}

function StatusPanel({ icon, tone, iconColor = colors.ink, title, detail }: { icon: FeatherIcon; tone: string; iconColor?: string; title: string; detail: string }) {
  return (
    <View style={styles.resolution}>
      <View style={[styles.resolutionIcon, { backgroundColor: tone }]}><Feather color={iconColor} name={icon} size={21} /></View>
      <Text style={styles.resolutionTitle}>{title}</Text>
      <Text style={styles.resolutionDetail}>{detail}</Text>
    </View>
  );
}

export default function MatchRoomScreen() {
  const params = useLocalSearchParams<{ id?: string | string[] }>();
  const matchId = Array.isArray(params.id) ? params.id[0] : params.id;
  const match = useMemo(() => getDemoMatchRoom(matchId), [matchId]);
  const [lifecycle, setLifecycle] = useState<MatchLifecycle>(match.lifecycle);
  const [allowedActions, setAllowedActions] = useState<MatchAllowedAction[]>(match.allowedActions);
  const [verification, setVerification] = useState<ResultVerification>(match.resultVerification);
  const [draft, setDraft] = useState<ScoreDraft>(emptyDraft);
  const [screenshots, setScreenshots] = useState<LocalEvidenceAsset[]>([]);
  const [declared, setDeclared] = useState(false);
  const [formError, setFormError] = useState<string | null>(null);

  const opponent = match.currentPlayerSide === 'home' ? match.away : match.home;
  const checkedIn = lifecycle !== 'ready_for_check_in' && lifecycle !== 'assigned';
  const canCheckIn = allowedActions.includes('check_in');
  const canReport = allowedActions.includes('report_score');
  const canSubmitFinal = allowedActions.includes('submit_final_score');
  const reporting = lifecycle === 'assigned' || lifecycle === 'ready_for_check_in' || lifecycle === 'checked_in' || lifecycle === 'report_required';
  const penaltiesRequired = !match.drawAllowed && draft.home !== '' && draft.away !== '' && Number(draft.home) === Number(draft.away);

  function fail(message: string): null {
    setFormError(message);
    return null;
  }

  function readScore(): ScoreClaim | null {
    if (draft.home === '' || draft.away === '') return fail('Enter both final scores.');
    let tiebreak: ScoreTiebreak | null = null;
    if (penaltiesRequired) {
      if (draft.homePenalties === '' || draft.awayPenalties === '' || Number(draft.homePenalties) === Number(draft.awayPenalties)) {
        return fail('This round needs a winner. Enter the penalty shootout score.');
      }
      tiebreak = { type: 'penalties', homeScore: Number(draft.homePenalties), awayScore: Number(draft.awayPenalties) };
    }
    if (!declared) return fail('Confirm the declaration before sending your score.');
    return { homeScore: Number(draft.home), awayScore: Number(draft.away), tiebreak };
  }

  function moveTo(next: MatchLifecycle, actions: MatchAllowedAction[]) {
    setLifecycle(next);
    setAllowedActions(actions);
    setDraft(emptyDraft);
    setDeclared(false);
    setFormError(null);
  }

  function checkIn() {
    // The demo treats the opponent as checked in, so reporting opens at once.
    moveTo('report_required', ['report_score']);
  }

  function reportScore() {
    const claim = readScore();
    if (!claim) return;
    setVerification({
      ...verification,
      phase: 'awaiting_second_report',
      myReport: localReport('initial', claim),
      reportDeadline: new Date(Date.now() + DEMO_WINDOW_MS).toISOString(),
    });
    moveTo('awaiting_opponent_report', []);
  }

  function submitFinalScore() {
    if (screenshots.length === 0) {
      setFormError('Attach at least one screenshot of the final result.');
      return;
    }
    const claim = readScore();
    if (!claim) return;
    setVerification({ ...verification, myFinalReport: localReport('final', claim) });
    moveTo('awaiting_opponent_response', []);
  }

  async function pickScreenshot() {
    setFormError(null);
    const result = await ImagePicker.launchImageLibraryAsync({
      mediaTypes: ['images'],
      allowsEditing: false,
      quality: 0.9,
    });
    if (result.canceled) return;

    const selected = result.assets[0];
    const contentType = selected.mimeType ?? 'image/jpeg';
    if (!SCREENSHOT_MEDIA_TYPES.includes(contentType)) {
      setFormError('Choose a JPEG or PNG screenshot. Convert HEIC photos to JPEG first.');
      return;
    }
    if (selected.fileSize && selected.fileSize > MAX_SCREENSHOT_BYTES) {
      setFormError('Choose a screenshot smaller than 10 MB.');
      return;
    }
    setScreenshots((current) => [...current, {
      uri: selected.uri,
      fileName: selected.fileName ?? `match-${match.id}-result-${current.length + 1}.jpg`,
      contentType,
      byteSize: selected.fileSize,
      width: selected.width,
      height: selected.height,
    }].slice(0, MAX_FINAL_SCREENSHOTS));
  }

  function removeScreenshot(uri: string) {
    setScreenshots((current) => current.filter((shot) => shot.uri !== uri));
  }

  function renderStatus() {
    const { myReport, myFinalReport } = verification;
    switch (lifecycle) {
      case 'awaiting_opponent_report':
        return (
          <View style={styles.flowSection}>
            <StatusPanel
              detail={`${opponent.handle} has until ${formatDeadline(verification.reportDeadline)} to report. Matching scores confirm the result at once. If they don't report in time, they're removed from the tournament and you win by forfeit.`}
              icon="clock"
              iconColor={colors.paper}
              title={`Waiting for ${opponent.handle}`}
              tone={colors.blue}
            />
            {myReport ? <OwnScore awayHandle={match.away.handle} homeHandle={match.home.handle} label="YOUR REPORT" report={myReport} /> : null}
            <View style={styles.timeline}>
              <Instruction complete detail="Only you can see the score you sent" number={1} title="Score reported" />
              <Instruction detail="Your opponent reports without seeing your score" number={2} title="Opponent reports" />
              <Instruction detail="Matching scores confirm the result and advance the bracket" number={3} title="Scores compared" />
            </View>
          </View>
        );
      case 'mismatch_response_required':
        return (
          <View style={[styles.flowSection, !canSubmitFinal && styles.disabledSection]} pointerEvents={canSubmitFinal ? 'auto' : 'none'}>
            <View style={styles.sectionHeader}>
              <View><Text style={styles.eyebrow}>SCORES DON&apos;T MATCH</Text><Text style={styles.sectionTitle}>Submit your final score</Text></View>
              <View style={styles.actionBadge}><Text style={styles.actionBadgeText}>ACTION NEEDED</Text></View>
            </View>
            <Text style={styles.sectionBody}>
              Your report and your opponent&apos;s report differ. Neither side sees the other&apos;s score. Send your final score once, with one to three screenshots of the final result, before {formatDeadline(verification.responseDeadline)}. If you don&apos;t, you&apos;re removed from the tournament.
            </Text>
            {myReport ? <OwnScore awayHandle={match.away.handle} homeHandle={match.home.handle} label="YOUR FIRST REPORT" report={myReport} /> : null}
            <ScoreFields awayLabel={match.away.handle} draft={draft} homeLabel={match.home.handle} onChange={setDraft} penaltiesRequired={penaltiesRequired} />

            <Text style={styles.inputLabel}>FINAL-RESULT SCREENSHOTS · {screenshots.length}/{MAX_FINAL_SCREENSHOTS}</Text>
            {screenshots.map((shot) => (
              <View key={shot.uri} style={styles.shotRow}>
                <Image accessibilityLabel={`Selected screenshot ${shot.fileName}`} resizeMode="cover" source={{ uri: shot.uri }} style={styles.shotThumb} />
                <View style={styles.previewCopy}><Text numberOfLines={1} style={styles.previewName}>{shot.fileName}</Text><Text style={styles.previewMeta}>JPEG or PNG · max 10 MB</Text></View>
                <Pressable accessibilityLabel="Remove screenshot" hitSlop={8} onPress={() => removeScreenshot(shot.uri)} style={styles.removeButton}><Feather color={colors.paper} name="trash-2" size={18} /></Pressable>
              </View>
            ))}
            {screenshots.length < MAX_FINAL_SCREENSHOTS ? (
              <Pressable accessibilityHint="Opens your photo library" accessibilityLabel="Add result screenshot" onPress={pickScreenshot} style={styles.uploadButton}>
                <View style={styles.uploadIcon}><Feather color={colors.acid} name="image" size={21} /></View>
                <View style={styles.uploadCopy}><Text style={styles.uploadTitle}>Add screenshot</Text><Text style={styles.uploadHint}>JPEG or PNG · max 10 MB · up to three</Text></View>
                <Feather color={colors.muted} name="plus" size={20} />
              </Pressable>
            ) : null}

            <Declaration checked={declared} onToggle={() => setDeclared((current) => !current)} text="I declare that this final score and these screenshots are accurate and from this match." />
            {formError ? <Text accessibilityLiveRegion="polite" style={styles.error}>{formError}</Text> : null}
            <Pressable onPress={submitFinalScore} style={styles.primaryWide}>
              <Text style={styles.primaryWideText}>Submit final score</Text>
              <Feather color={colors.ink} name="send" size={17} />
            </Pressable>
            <Text style={styles.backendNote}>Prototype flow only · screenshots are not uploaded and nothing is sent to Gamics.</Text>
          </View>
        );
      case 'awaiting_opponent_response':
        return (
          <View style={styles.flowSection}>
            <StatusPanel
              detail={`If ${opponent.handle}'s final score matches yours, the result is confirmed. If it still differs, Gamics reviews the match. If they don't respond by ${formatDeadline(verification.responseDeadline)}, they're removed from the tournament.`}
              icon="clock"
              iconColor={colors.paper}
              title="Final score submitted"
              tone={colors.blue}
            />
            {myFinalReport ? <OwnScore awayHandle={match.away.handle} homeHandle={match.home.handle} label="YOUR FINAL SCORE" report={myFinalReport} /> : null}
          </View>
        );
      case 'awaiting_resolution':
        return (
          <View style={styles.flowSection}>
            <StatusPanel detail="A deadline has passed and Gamics is settling this match. You'll be notified of the outcome shortly." icon="clock" iconColor={colors.paper} title="Settling the result" tone={colors.blue} />
          </View>
        );
      case 'under_review':
        return (
          <View style={styles.flowSection}>
            <StatusPanel detail="The final scores still differ, so Gamics staff are reviewing the screenshots. Organizers and your opponent can't see yours. You'll be notified when a decision is made." icon="shield" title="Under Gamics review" tone={colors.orange} />
          </View>
        );
      default:
        return (
          <View style={styles.flowSection}>
            <StatusPanel
              detail={verification.entryRemoved
                ? 'Your entry was removed from this tournament because a score or final score was not sent in time, or after a Gamics review.'
                : 'This match is over. The confirmed result is part of the bracket.'}
              icon={verification.entryRemoved ? 'user-x' : 'check'}
              title={verification.entryRemoved ? 'Removed from tournament' : 'Match over'}
              tone={verification.entryRemoved ? colors.orange : colors.acid}
            />
            {match.result ? <OwnScore awayHandle={match.away.handle} homeHandle={match.home.handle} label="CONFIRMED RESULT" report={match.result} /> : null}
          </View>
        );
    }
  }

  return (
    <SafeAreaView style={styles.safeArea}>
      <KeyboardAvoidingView behavior={Platform.OS === 'ios' ? 'padding' : undefined} style={styles.flex}>
        <ScrollView contentContainerStyle={styles.content} keyboardShouldPersistTaps="handled" showsVerticalScrollIndicator={false}>
          <View style={styles.topbar}>
            <Pressable accessibilityLabel="Go back" hitSlop={10} onPress={() => router.back()} style={styles.iconButton}>
              <Feather color={colors.paper} name="arrow-left" size={22} />
            </Pressable>
            <View style={styles.topbarCopy}>
              <Text style={styles.matchCode}>{match.code}</Text>
              <Text numberOfLines={1} style={styles.competition}>{match.competitionName}</Text>
            </View>
            <View accessibilityLabel="Demo data; backend API pending" style={styles.demoBadge}>
              <View style={styles.demoDot} />
              <Text style={styles.demoBadgeText}>DEMO · API PENDING</Text>
            </View>
          </View>

          <View style={styles.versusRow}>
            <View style={styles.competitor}><Avatar player={match.home} /><Text numberOfLines={1} style={styles.handle}>{match.home.handle}</Text></View>
            <View style={styles.versusCopy}><Text style={styles.round}>{match.roundName}</Text><Text style={styles.vs}>VS</Text><Text style={styles.game}>{match.gameName}</Text></View>
            <View style={styles.competitor}><Avatar accent={colors.orange} player={match.away} /><Text numberOfLines={1} style={styles.handle}>{match.away.handle}</Text></View>
          </View>

          {reporting ? (
            <>
              <View style={styles.flowSection}>
                <View style={styles.sectionHeader}><View><Text style={styles.eyebrow}>STEP 1</Text><Text style={styles.sectionTitle}>Check in</Text></View>{checkedIn ? <Feather color={colors.green} name="check-circle" size={23} /> : null}</View>
                <Text style={styles.sectionBody}>Both players must check in before the room closes. This prevents no-shows from blocking the bracket.</Text>
                <Pressable
                  accessibilityState={{ disabled: !canCheckIn }}
                  disabled={!canCheckIn}
                  onPress={checkIn}
                  style={[styles.primaryWide, checkedIn && styles.buttonComplete, !canCheckIn && !checkedIn && styles.buttonUnavailable]}
                >
                  <Text style={styles.primaryWideText}>{checkedIn ? 'Demo check-in recorded' : canCheckIn ? 'Check in (demo)' : 'Check-in unavailable'}</Text>
                  <Feather color={colors.ink} name={checkedIn ? 'check' : 'arrow-right'} size={18} />
                </Pressable>
              </View>

              <View style={styles.flowSection}>
                <View><Text style={styles.eyebrow}>STEP 2</Text><Text style={styles.sectionTitle}>Play the match</Text></View>
                <View style={styles.timeline}>
                  {match.friendMatchInstructions.map((instruction, index) => (
                    <Instruction
                      complete={index === 0 && checkedIn}
                      detail={instruction.detail}
                      key={instruction.title}
                      number={index + 1}
                      title={instruction.title}
                    />
                  ))}
                </View>
              </View>

              <View style={[styles.flowSection, !canReport && styles.disabledSection]} pointerEvents={canReport ? 'auto' : 'none'}>
                <View><Text style={styles.eyebrow}>STEP 3</Text><Text style={styles.sectionTitle}>Report the score</Text></View>
                <Text style={styles.sectionBody}>
                  Report the score only; no screenshot is needed yet. Your opponent can&apos;t see your score and you won&apos;t see theirs. Matching scores confirm the result at once. After the first report, the other side has {Math.round(DEMO_WINDOW_MS / 60000)} minutes to report or is removed from the tournament.
                </Text>
                <ScoreFields awayLabel={match.away.handle} draft={draft} homeLabel={match.home.handle} onChange={setDraft} penaltiesRequired={penaltiesRequired} />
                <Declaration checked={declared} onToggle={() => setDeclared((current) => !current)} text="I declare that this is the final score of this match." />
                {formError ? <Text accessibilityLiveRegion="polite" style={styles.error}>{formError}</Text> : null}
                <Pressable onPress={reportScore} style={styles.primaryWide}>
                  <Text style={styles.primaryWideText}>Report score</Text>
                  <Feather color={colors.ink} name="send" size={17} />
                </Pressable>
                <Text style={styles.backendNote}>Prototype flow only · score reports are not sent to Gamics yet.</Text>
              </View>
            </>
          ) : (
            renderStatus()
          )}
        </ScrollView>
      </KeyboardAvoidingView>
    </SafeAreaView>
  );
}

const styles = StyleSheet.create({
  flex: { flex: 1 },
  safeArea: { flex: 1, backgroundColor: colors.ink },
  content: { paddingHorizontal: spacing.md, paddingBottom: spacing.xxl, gap: spacing.md },
  topbar: { minHeight: 56, flexDirection: 'row', alignItems: 'center', gap: 11 },
  iconButton: { width: 42, height: 42, borderRadius: 21, alignItems: 'center', justifyContent: 'center', backgroundColor: colors.panel },
  topbarCopy: { flex: 1, minWidth: 0 },
  matchCode: { color: colors.acid, fontFamily: fonts.mono, fontSize: 9, fontWeight: '900' },
  competition: { color: colors.paper, marginTop: 2, fontSize: 14, fontWeight: '700' },
  demoBadge: { flexDirection: 'row', alignItems: 'center', gap: 5, paddingHorizontal: 8, paddingVertical: 6, borderRadius: 999, backgroundColor: colors.panel },
  demoDot: { width: 6, height: 6, borderRadius: 3, backgroundColor: colors.orange },
  demoBadgeText: { color: colors.muted, fontFamily: fonts.mono, fontSize: 6.5, fontWeight: '900' },
  versusRow: { minHeight: 142, flexDirection: 'row', alignItems: 'center', justifyContent: 'space-around', paddingVertical: spacing.md, borderBottomWidth: StyleSheet.hairlineWidth, borderBottomColor: colors.line },
  competitor: { width: 94, alignItems: 'center', gap: 7 },
  avatar: { width: 58, height: 58, borderRadius: 29, alignItems: 'center', justifyContent: 'center' },
  avatarText: { color: colors.paper, fontSize: 20, fontWeight: '900' },
  handle: { width: '100%', color: colors.paper, fontFamily: fonts.mono, fontSize: 9, fontWeight: '800', textAlign: 'center' },
  versusCopy: { width: 92, alignItems: 'center' },
  round: { color: colors.muted, fontFamily: fonts.mono, fontSize: 8, textTransform: 'uppercase' },
  vs: { color: colors.paper, marginVertical: 4, fontSize: 22, fontWeight: '900' },
  game: { color: colors.muted, fontSize: 9, textAlign: 'center' },
  flowSection: { gap: spacing.md, paddingVertical: spacing.md },
  disabledSection: { opacity: 0.42 },
  sectionHeader: { flexDirection: 'row', alignItems: 'flex-start', justifyContent: 'space-between', gap: spacing.md },
  eyebrow: { color: colors.acid, marginBottom: 3, fontFamily: fonts.mono, fontSize: 8, fontWeight: '900', letterSpacing: 0.8 },
  sectionTitle: { color: colors.paper, fontSize: 22, fontWeight: '800', letterSpacing: -0.3 },
  sectionBody: { color: colors.muted, fontSize: 13, lineHeight: 19 },
  actionBadge: { paddingHorizontal: 9, paddingVertical: 6, borderRadius: 999, backgroundColor: colors.orange },
  actionBadgeText: { color: colors.ink, fontFamily: fonts.mono, fontSize: 7, fontWeight: '900' },
  primaryWide: { minHeight: 52, paddingHorizontal: spacing.md, flexDirection: 'row', alignItems: 'center', justifyContent: 'space-between', borderRadius: radius.md, backgroundColor: colors.acid },
  primaryWideText: { color: colors.ink, fontSize: 14, fontWeight: '900' },
  buttonComplete: { backgroundColor: colors.green },
  buttonUnavailable: { opacity: 0.45 },
  timeline: { gap: 3 },
  instruction: { flexDirection: 'row', gap: 12, paddingVertical: 10 },
  instructionNumber: { width: 28, height: 28, borderRadius: 14, alignItems: 'center', justifyContent: 'center', borderWidth: 1, borderColor: colors.line },
  instructionComplete: { borderColor: colors.acid, backgroundColor: colors.acid },
  instructionNumberText: { color: colors.paper, fontFamily: fonts.mono, fontSize: 10, fontWeight: '800' },
  instructionCopy: { flex: 1 },
  instructionTitle: { color: colors.paper, fontSize: 13, fontWeight: '700' },
  instructionDetail: { color: colors.muted, marginTop: 2, fontSize: 11, lineHeight: 16 },
  inputLabel: { color: colors.muted, marginTop: 3, fontFamily: fonts.mono, fontSize: 8, fontWeight: '800', letterSpacing: 0.8 },
  scoreEntry: { flexDirection: 'row', alignItems: 'flex-end', justifyContent: 'center', gap: 12 },
  scoreSide: { width: 102, alignItems: 'center', gap: 7 },
  scoreLabel: { width: '100%', color: colors.paper, fontFamily: fonts.mono, fontSize: 9, textAlign: 'center' },
  scoreInput: { width: 76, height: 64, borderRadius: radius.md, color: colors.paper, backgroundColor: colors.panel, borderWidth: 1, borderColor: colors.line, fontFamily: fonts.mono, fontSize: 29, fontWeight: '900', textAlign: 'center' },
  scoreSeparator: { color: colors.muted, paddingBottom: 17, fontSize: 25, fontWeight: '700' },
  penaltiesBlock: { alignItems: 'center', gap: 8, paddingVertical: spacing.sm, borderRadius: radius.md, backgroundColor: colors.panel },
  penaltyRow: { flexDirection: 'row', alignItems: 'center', gap: 10 },
  penaltyInput: { width: 58, height: 46, borderRadius: radius.sm, color: colors.paper, backgroundColor: colors.ink, fontFamily: fonts.mono, fontSize: 20, fontWeight: '800', textAlign: 'center' },
  penaltySeparator: { color: colors.muted, fontSize: 18 },
  uploadButton: { minHeight: 70, padding: 12, flexDirection: 'row', alignItems: 'center', gap: 12, borderRadius: radius.md, borderWidth: 1, borderStyle: 'dashed', borderColor: colors.line },
  uploadIcon: { width: 42, height: 42, borderRadius: 21, alignItems: 'center', justifyContent: 'center', backgroundColor: colors.panel },
  uploadCopy: { flex: 1 },
  uploadTitle: { color: colors.paper, fontSize: 13, fontWeight: '800' },
  uploadHint: { color: colors.muted, marginTop: 3, fontSize: 10 },
  shotRow: { minHeight: 68, paddingHorizontal: 10, flexDirection: 'row', alignItems: 'center', gap: 10, borderRadius: radius.md, backgroundColor: colors.panel },
  shotThumb: { width: 48, height: 48, borderRadius: radius.sm, backgroundColor: colors.subtleInk },
  previewCopy: { flex: 1, minWidth: 0 },
  previewName: { color: colors.paper, fontSize: 12, fontWeight: '700' },
  previewMeta: { color: colors.muted, marginTop: 2, fontSize: 9 },
  removeButton: { width: 40, height: 40, alignItems: 'center', justifyContent: 'center' },
  declaration: { minHeight: 54, flexDirection: 'row', alignItems: 'center', gap: 11 },
  checkbox: { width: 24, height: 24, borderRadius: 6, alignItems: 'center', justifyContent: 'center', borderWidth: 1, borderColor: colors.muted },
  checkboxChecked: { borderColor: colors.acid, backgroundColor: colors.acid },
  declarationText: { flex: 1, color: colors.paper, fontSize: 12, lineHeight: 17 },
  error: { color: colors.orange, fontSize: 12, lineHeight: 17 },
  backendNote: { color: colors.muted, fontSize: 9, lineHeight: 14, textAlign: 'center' },
  ownScore: { gap: 6 },
  readOnlyScore: { minHeight: 112, flexDirection: 'row', alignItems: 'center', justifyContent: 'center', gap: spacing.md, borderRadius: radius.md, backgroundColor: colors.panel },
  readOnlyPlayer: { width: 100, alignItems: 'center' },
  readOnlyHandle: { width: '100%', color: colors.muted, fontFamily: fonts.mono, fontSize: 9, textAlign: 'center' },
  readOnlyNumber: { color: colors.paper, marginTop: 4, fontFamily: fonts.mono, fontSize: 34, fontWeight: '900' },
  scoreDash: { color: colors.muted, fontSize: 24 },
  tiebreakNote: { color: colors.muted, fontFamily: fonts.mono, fontSize: 10, textAlign: 'center' },
  resolution: { alignItems: 'center', paddingVertical: spacing.lg },
  resolutionIcon: { width: 52, height: 52, borderRadius: 26, alignItems: 'center', justifyContent: 'center', marginBottom: 13 },
  resolutionTitle: { color: colors.paper, fontSize: 21, fontWeight: '900', textAlign: 'center' },
  resolutionDetail: { maxWidth: 310, color: colors.muted, marginTop: 7, fontSize: 12, lineHeight: 18, textAlign: 'center' },
});
