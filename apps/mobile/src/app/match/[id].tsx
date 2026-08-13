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
import type { LocalEvidenceAsset, MatchLifecycle, MatchParticipant, MatchScore } from '@/features/matches/types';

const MAX_SCREENSHOT_BYTES = 10 * 1024 * 1024;

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
        onChangeText={(next) => onChangeText(next.replace(/\D/g, ''))}
        placeholder="0"
        placeholderTextColor={colors.subtleInk}
        selectTextOnFocus
        style={styles.scoreInput}
        value={value}
      />
    </View>
  );
}

function ReadOnlyScore({ home, away, homeHandle, awayHandle }: { home: number; away: number; homeHandle: string; awayHandle: string }) {
  return (
    <View style={styles.readOnlyScore}>
      <View style={styles.readOnlyPlayer}><Text numberOfLines={1} style={styles.readOnlyHandle}>{homeHandle}</Text><Text style={styles.readOnlyNumber}>{home}</Text></View>
      <Text style={styles.scoreDash}>–</Text>
      <View style={styles.readOnlyPlayer}><Text numberOfLines={1} style={styles.readOnlyHandle}>{awayHandle}</Text><Text style={styles.readOnlyNumber}>{away}</Text></View>
    </View>
  );
}

export default function MatchRoomScreen() {
  const params = useLocalSearchParams<{ id?: string | string[] }>();
  const matchId = Array.isArray(params.id) ? params.id[0] : params.id;
  const match = useMemo(() => getDemoMatchRoom(matchId), [matchId]);
  const [lifecycle, setLifecycle] = useState<MatchLifecycle>(match.lifecycle);
  const [homeScore, setHomeScore] = useState('');
  const [awayScore, setAwayScore] = useState('');
  const [homePenalties, setHomePenalties] = useState('');
  const [awayPenalties, setAwayPenalties] = useState('');
  const [evidence, setEvidence] = useState<LocalEvidenceAsset | null>(null);
  const [declared, setDeclared] = useState(false);
  const [formError, setFormError] = useState<string | null>(null);
  const [submittedScore, setSubmittedScore] = useState<MatchScore | null>(null);
  const [reviewMode, setReviewMode] = useState<'none' | 'dispute'>('none');
  const [disputeDetails, setDisputeDetails] = useState('');
  const [reviewResolution, setReviewResolution] = useState<'confirmed' | 'disputed' | null>(null);

  const canConfirmResult = match.allowedActions.includes('confirm_result');
  const canDisputeResult = match.allowedActions.includes('dispute_result');
  const canReviewResult = canConfirmResult || canDisputeResult;
  const incomingResult = canReviewResult && match.result;
  const checkedIn = lifecycle !== 'ready_for_check_in' && lifecycle !== 'assigned';
  const canCheckIn = match.allowedActions.includes('check_in') && !checkedIn;
  const canSubmitResult = match.allowedActions.includes('submit_result') || lifecycle === 'checked_in';
  const scoresAreTied = homeScore !== '' && awayScore !== '' && Number(homeScore) === Number(awayScore);

  async function pickScreenshot() {
    setFormError(null);
    const result = await ImagePicker.launchImageLibraryAsync({
      mediaTypes: ['images'],
      allowsEditing: false,
      quality: 0.9,
    });
    if (result.canceled) return;

    const selected = result.assets[0];
    if (selected.fileSize && selected.fileSize > MAX_SCREENSHOT_BYTES) {
      setFormError('Choose a screenshot smaller than 10 MB.');
      return;
    }
    setEvidence({
      uri: selected.uri,
      fileName: selected.fileName ?? `match-${match.id}-result.jpg`,
      contentType: selected.mimeType ?? 'image/jpeg',
      byteSize: selected.fileSize,
      width: selected.width,
      height: selected.height,
    });
  }

  function checkIn() {
    setLifecycle('checked_in');
    setFormError(null);
  }

  function submitResult() {
    if (!checkedIn) {
      setFormError('Check in before submitting a result.');
      return;
    }
    if (homeScore === '' || awayScore === '') {
      setFormError('Enter both final scores.');
      return;
    }

    const score: MatchScore = { home: Number(homeScore), away: Number(awayScore) };
    if (scoresAreTied && !match.drawAllowed) {
      if (homePenalties === '' || awayPenalties === '' || Number(homePenalties) === Number(awayPenalties)) {
        setFormError('This round needs a winner. Enter the penalty shootout score.');
        return;
      }
      score.tiebreak = { type: 'penalties', home: Number(homePenalties), away: Number(awayPenalties) };
    }
    if (!evidence) {
      setFormError('Attach the final-result screenshot.');
      return;
    }
    if (!declared) {
      setFormError('Accept the result declaration before submitting.');
      return;
    }

    setSubmittedScore(score);
    setLifecycle('awaiting_opponent');
    setFormError(null);
  }

  function submitDispute() {
    if (disputeDetails.trim().length < 10) {
      setFormError('Describe the problem in at least 10 characters.');
      return;
    }
    setReviewResolution('disputed');
    setLifecycle('under_review');
    setFormError(null);
  }

  const opponent = match.currentPlayerSide === 'home' ? match.away : match.home;

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

          {incomingResult ? (
            <View style={styles.flowSection}>
              {reviewResolution === 'confirmed' ? (
                <View style={styles.resolution}>
                  <View style={[styles.resolutionIcon, { backgroundColor: colors.acid }]}><Feather color={colors.ink} name="check" size={22} /></View>
                  <Text style={styles.resolutionTitle}>Demo confirmation recorded</Text>
                  <Text style={styles.resolutionDetail}>Nothing was sent to Gamics and no record or bracket was updated. The confirmation API is still pending.</Text>
                </View>
              ) : reviewResolution === 'disputed' ? (
                <View style={styles.resolution}>
                  <View style={[styles.resolutionIcon, { backgroundColor: colors.orange }]}><Feather color={colors.ink} name="flag" size={20} /></View>
                  <Text style={styles.resolutionTitle}>Demo dispute recorded</Text>
                  <Text style={styles.resolutionDetail}>Nothing was sent and no referee queue was created. The decision API must exist before a real match can be frozen.</Text>
                </View>
              ) : (
                <>
                  <View style={styles.sectionHeader}>
                    <View><Text style={styles.eyebrow}>OPPONENT SUBMITTED</Text><Text style={styles.sectionTitle}>Check the result</Text></View>
                    <View style={styles.actionBadge}><Text style={styles.actionBadgeText}>ACTION NEEDED</Text></View>
                  </View>
                  <Text style={styles.sectionBody}>Only confirm if the score and screenshot match the game you played.</Text>
                  <ReadOnlyScore home={incomingResult.score.home} away={incomingResult.score.away} homeHandle={match.home.handle} awayHandle={match.away.handle} />

                  <View style={styles.receivedEvidence}>
                    <View style={styles.receivedIcon}><Feather color={colors.acid} name="image" size={20} /></View>
                    <View style={styles.receivedCopy}><Text style={styles.receivedTitle}>Final-result screenshot</Text><Text numberOfLines={1} style={styles.receivedName}>{incomingResult.evidence[0]?.fileName}</Text></View>
                    <Feather color={colors.muted} name="maximize-2" size={18} />
                  </View>

                  {reviewMode === 'dispute' ? (
                    <View style={styles.disputeForm}>
                      <Text style={styles.inputLabel}>WHAT IS WRONG?</Text>
                      <TextInput
                        accessibilityLabel="Dispute details"
                        multiline
                        onChangeText={setDisputeDetails}
                        placeholder="Example: the screenshot shows 2–1, but I won 3–2."
                        placeholderTextColor={colors.subtleInk}
                        style={styles.disputeInput}
                        textAlignVertical="top"
                        value={disputeDetails}
                      />
                      {formError ? <Text accessibilityLiveRegion="polite" style={styles.error}>{formError}</Text> : null}
                      <View style={styles.reviewActions}>
                        <Pressable onPress={() => { setReviewMode('none'); setFormError(null); }} style={styles.secondaryButton}><Text style={styles.secondaryButtonText}>Cancel</Text></Pressable>
                        <Pressable onPress={submitDispute} style={styles.dangerButton}><Text style={styles.dangerButtonText}>Submit dispute</Text></Pressable>
                      </View>
                    </View>
                  ) : (
                    <View style={styles.reviewActions}>
                      <Pressable disabled={!canDisputeResult} onPress={() => setReviewMode('dispute')} style={[styles.secondaryButton, !canDisputeResult && styles.buttonUnavailable]}><Feather color={colors.paper} name="flag" size={16} /><Text style={styles.secondaryButtonText}>Dispute</Text></Pressable>
                      <Pressable disabled={!canConfirmResult} onPress={() => { setReviewResolution('confirmed'); setLifecycle('confirmed'); }} style={[styles.primaryButton, !canConfirmResult && styles.buttonUnavailable]}><Feather color={colors.ink} name="check" size={17} /><Text style={styles.primaryButtonText}>Confirm result</Text></Pressable>
                    </View>
                  )}
                </>
              )}
            </View>
          ) : lifecycle === 'awaiting_opponent' && submittedScore ? (
            <View style={styles.flowSection}>
              <View style={styles.resolution}>
                <View style={[styles.resolutionIcon, { backgroundColor: colors.blue }]}><Feather color={colors.paper} name="clock" size={21} /></View>
                <Text style={styles.resolutionTitle}>Waiting for {opponent.handle}</Text>
                <Text style={styles.resolutionDetail}>Your result is saved in this prototype. The signed upload and submission APIs are still required before it can be authoritative.</Text>
              </View>
              <ReadOnlyScore home={submittedScore.home} away={submittedScore.away} homeHandle={match.home.handle} awayHandle={match.away.handle} />
              <View style={styles.timeline}>
                <Instruction complete detail="Score, declaration and screenshot added" number={1} title="Result submitted" />
                <Instruction detail="Opponent can confirm or open a dispute" number={2} title="Opponent verification" />
                <Instruction detail="Confirmed result advances the bracket" number={3} title="Progression" />
              </View>
            </View>
          ) : (
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

              <View style={[styles.flowSection, !canSubmitResult && styles.disabledSection]} pointerEvents={canSubmitResult ? 'auto' : 'none'}>
                <View><Text style={styles.eyebrow}>STEP 3</Text><Text style={styles.sectionTitle}>Submit result</Text></View>
                <Text style={styles.inputLabel}>FINAL SCORE</Text>
                <View style={styles.scoreEntry}>
                  <ScoreInput label={match.home.handle} onChangeText={setHomeScore} value={homeScore} />
                  <Text style={styles.scoreSeparator}>–</Text>
                  <ScoreInput label={match.away.handle} onChangeText={setAwayScore} value={awayScore} />
                </View>

                {scoresAreTied && !match.drawAllowed ? (
                  <View style={styles.penaltiesBlock}>
                    <Text style={styles.inputLabel}>PENALTY SHOOTOUT</Text>
                    <View style={styles.penaltyRow}>
                      <TextInput accessibilityLabel={`${match.home.handle} penalties`} inputMode="numeric" keyboardType="number-pad" maxLength={2} onChangeText={(next) => setHomePenalties(next.replace(/\D/g, ''))} placeholder="0" placeholderTextColor={colors.subtleInk} style={styles.penaltyInput} value={homePenalties} />
                      <Text style={styles.penaltySeparator}>–</Text>
                      <TextInput accessibilityLabel={`${match.away.handle} penalties`} inputMode="numeric" keyboardType="number-pad" maxLength={2} onChangeText={(next) => setAwayPenalties(next.replace(/\D/g, ''))} placeholder="0" placeholderTextColor={colors.subtleInk} style={styles.penaltyInput} value={awayPenalties} />
                    </View>
                  </View>
                ) : null}

                <Text style={styles.inputLabel}>FINAL-RESULT SCREENSHOT</Text>
                {evidence ? (
                  <View style={styles.previewWrap}>
                    <Image accessibilityLabel="Selected result screenshot" resizeMode="cover" source={{ uri: evidence.uri }} style={styles.preview} />
                    <View style={styles.previewFooter}>
                      <View style={styles.previewCopy}><Text numberOfLines={1} style={styles.previewName}>{evidence.fileName}</Text><Text style={styles.previewMeta}>Ready to upload · max 10 MB</Text></View>
                      <Pressable accessibilityLabel="Remove screenshot" hitSlop={8} onPress={() => setEvidence(null)} style={styles.removeButton}><Feather color={colors.paper} name="trash-2" size={18} /></Pressable>
                    </View>
                  </View>
                ) : (
                  <Pressable accessibilityHint="Opens your photo library" accessibilityLabel="Add result screenshot" onPress={pickScreenshot} style={styles.uploadButton}>
                    <View style={styles.uploadIcon}><Feather color={colors.acid} name="image" size={21} /></View>
                    <View style={styles.uploadCopy}><Text style={styles.uploadTitle}>Choose screenshot</Text><Text style={styles.uploadHint}>JPG, PNG or HEIC · max 10 MB</Text></View>
                    <Feather color={colors.muted} name="plus" size={20} />
                  </Pressable>
                )}

                <Pressable accessibilityRole="checkbox" accessibilityState={{ checked: declared }} onPress={() => setDeclared((current) => !current)} style={styles.declaration}>
                  <View style={[styles.checkbox, declared && styles.checkboxChecked]}>{declared ? <Feather color={colors.ink} name="check" size={14} /> : null}</View>
                  <Text style={styles.declarationText}>I declare that this score and screenshot are accurate and from this match.</Text>
                </Pressable>

                {formError ? <Text accessibilityLiveRegion="polite" style={styles.error}>{formError}</Text> : null}
                <Pressable onPress={submitResult} style={styles.primaryWide}>
                  <Text style={styles.primaryWideText}>Submit for confirmation</Text>
                  <Feather color={colors.ink} name="send" size={17} />
                </Pressable>
                <Text style={styles.backendNote}>Prototype flow only · signed evidence upload and result APIs are not live yet.</Text>
              </View>
            </>
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
  previewWrap: { overflow: 'hidden', borderRadius: radius.md, backgroundColor: colors.panel },
  preview: { width: '100%', aspectRatio: 16 / 9, backgroundColor: colors.subtleInk },
  previewFooter: { minHeight: 56, paddingHorizontal: 12, flexDirection: 'row', alignItems: 'center', gap: 10 },
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
  readOnlyScore: { minHeight: 112, flexDirection: 'row', alignItems: 'center', justifyContent: 'center', gap: spacing.md, borderRadius: radius.md, backgroundColor: colors.panel },
  readOnlyPlayer: { width: 100, alignItems: 'center' },
  readOnlyHandle: { width: '100%', color: colors.muted, fontFamily: fonts.mono, fontSize: 9, textAlign: 'center' },
  readOnlyNumber: { color: colors.paper, marginTop: 4, fontFamily: fonts.mono, fontSize: 34, fontWeight: '900' },
  scoreDash: { color: colors.muted, fontSize: 24 },
  receivedEvidence: { minHeight: 68, paddingHorizontal: 12, flexDirection: 'row', alignItems: 'center', gap: 11, borderRadius: radius.md, backgroundColor: colors.panel },
  receivedIcon: { width: 40, height: 40, borderRadius: 20, alignItems: 'center', justifyContent: 'center', backgroundColor: colors.ink },
  receivedCopy: { flex: 1, minWidth: 0 },
  receivedTitle: { color: colors.paper, fontSize: 12, fontWeight: '700' },
  receivedName: { color: colors.muted, marginTop: 3, fontSize: 10 },
  reviewActions: { flexDirection: 'row', gap: spacing.sm },
  secondaryButton: { minHeight: 50, flex: 1, paddingHorizontal: 13, flexDirection: 'row', alignItems: 'center', justifyContent: 'center', gap: 7, borderRadius: radius.md, borderWidth: 1, borderColor: colors.line },
  secondaryButtonText: { color: colors.paper, fontSize: 13, fontWeight: '800' },
  primaryButton: { minHeight: 50, flex: 1.35, paddingHorizontal: 13, flexDirection: 'row', alignItems: 'center', justifyContent: 'center', gap: 7, borderRadius: radius.md, backgroundColor: colors.acid },
  primaryButtonText: { color: colors.ink, fontSize: 13, fontWeight: '900' },
  disputeForm: { gap: spacing.md },
  disputeInput: { minHeight: 116, padding: 12, borderRadius: radius.md, color: colors.paper, backgroundColor: colors.panel, borderWidth: 1, borderColor: colors.line, fontSize: 13, lineHeight: 19 },
  dangerButton: { minHeight: 50, flex: 1.5, alignItems: 'center', justifyContent: 'center', borderRadius: radius.md, backgroundColor: colors.orange },
  dangerButtonText: { color: colors.ink, fontSize: 13, fontWeight: '900' },
  resolution: { alignItems: 'center', paddingVertical: spacing.lg },
  resolutionIcon: { width: 52, height: 52, borderRadius: 26, alignItems: 'center', justifyContent: 'center', marginBottom: 13 },
  resolutionTitle: { color: colors.paper, fontSize: 21, fontWeight: '900', textAlign: 'center' },
  resolutionDetail: { maxWidth: 310, color: colors.muted, marginTop: 7, fontSize: 12, lineHeight: 18, textAlign: 'center' },
});
