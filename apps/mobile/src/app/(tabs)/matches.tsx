import { Feather } from '@expo/vector-icons';
import { Link } from 'expo-router';
import { Pressable, ScrollView, StyleSheet, Text, View } from 'react-native';
import { SafeAreaView } from 'react-native-safe-area-context';

import { colors, fonts, radius, spacing } from '@/design/tokens';
import { demoAssignedMatches, demoMatchHistory } from '@/features/matches/demo';
import type { MatchHistoryItem, MatchSummary } from '@/features/matches/types';

function statusCopy(match: MatchSummary) {
  if (match.allowedActions.includes('confirm_result') || match.allowedActions.includes('dispute_result')) {
    return { label: 'Confirm result', tone: colors.orange };
  }
  if (match.allowedActions.includes('check_in')) {
    return { label: 'Check-in open', tone: colors.acid };
  }
  switch (match.lifecycle) {
    case 'awaiting_opponent':
      return { label: 'Awaiting opponent', tone: colors.blue };
    case 'disputed':
    case 'under_review':
      return { label: 'Under review', tone: colors.orange };
    default:
      return { label: 'Scheduled', tone: colors.muted };
  }
}

function MatchRow({ match }: { match: MatchSummary }) {
  const opponent = match.currentPlayerSide === 'home' ? match.away : match.home;
  const status = statusCopy(match);

  return (
    <Link href={{ pathname: '/match/[id]', params: { id: match.id } }} asChild>
      <Pressable
        accessibilityLabel={`Open match against ${opponent.displayName}`}
        accessibilityHint={status.label}
        style={({ pressed }) => [styles.matchRow, pressed && styles.pressed]}
      >
        <View style={styles.avatar}>
          <Text style={styles.avatarText}>{opponent.initials}</Text>
        </View>
        <View style={styles.matchCopy}>
          <View style={styles.statusLine}>
            <View style={[styles.statusDot, { backgroundColor: status.tone }]} />
            <Text style={[styles.status, { color: status.tone }]}>{status.label}</Text>
            <Text style={styles.code}>{match.code}</Text>
          </View>
          <Text numberOfLines={1} style={styles.opponent}>{opponent.handle}</Text>
          <Text numberOfLines={1} style={styles.meta}>{match.roundName} · {match.competitionName}</Text>
        </View>
        <Feather color={colors.muted} name="chevron-right" size={20} />
      </Pressable>
    </Link>
  );
}

function HistoryRow({ item }: { item: MatchHistoryItem }) {
  const isWin = item.outcome === 'win';
  const playerScore = item.opponent.side === 'away' ? item.score.home : item.score.away;
  const opponentScore = item.opponent.side === 'away' ? item.score.away : item.score.home;

  return (
    <View style={styles.historyRow}>
      <View style={[styles.form, isWin ? styles.win : styles.loss]}>
        <Text style={styles.formText}>{isWin ? 'W' : 'L'}</Text>
      </View>
      <View style={styles.historyCopy}>
        <Text numberOfLines={1} style={styles.historyOpponent}>{item.opponent.handle}</Text>
        <Text numberOfLines={1} style={styles.historyMeta}>{item.competitionName}</Text>
      </View>
      <View style={styles.scoreCopy}>
        <Text style={styles.historyScore}>{playerScore}–{opponentScore}</Text>
        <Text style={[styles.ratingDelta, { color: item.ratingDelta >= 0 ? colors.green : colors.orange }]}>
          {item.ratingDelta >= 0 ? '+' : ''}{item.ratingDelta}
        </Text>
      </View>
    </View>
  );
}

export default function MatchesScreen() {
  const needsAction = demoAssignedMatches.filter((match) => match.lifecycle === 'opponent_action_required');
  const upcoming = demoAssignedMatches.filter((match) => match.lifecycle !== 'opponent_action_required');

  return (
    <SafeAreaView style={styles.safeArea} edges={['top']}>
      <ScrollView contentContainerStyle={styles.content} showsVerticalScrollIndicator={false}>
        <View style={styles.header}>
          <View>
            <Text style={styles.title}>Matches</Text>
            <Text style={styles.subtitle}>Play, report, verify.</Text>
          </View>
          <View style={styles.demoBadge}>
            <View style={styles.demoDot} />
            <Text style={styles.demoText}>API PENDING</Text>
          </View>
        </View>

        {needsAction.length > 0 ? (
          <View style={styles.section}>
            <Text style={styles.sectionTitle}>Needs your action</Text>
            <View style={styles.list}>{needsAction.map((match) => <MatchRow key={match.id} match={match} />)}</View>
          </View>
        ) : null}

        <View style={styles.section}>
          <Text style={styles.sectionTitle}>Up next</Text>
          <View style={styles.list}>{upcoming.map((match) => <MatchRow key={match.id} match={match} />)}</View>
        </View>

        <View style={styles.section}>
          <View style={styles.sectionHeadingRow}>
            <Text style={styles.sectionTitle}>Recent</Text>
            <Text style={styles.historyCount}>{demoMatchHistory.length} matches</Text>
          </View>
          <View>{demoMatchHistory.map((item) => <HistoryRow item={item} key={item.matchId} />)}</View>
        </View>
      </ScrollView>
    </SafeAreaView>
  );
}

const styles = StyleSheet.create({
  safeArea: { flex: 1, backgroundColor: colors.ink },
  content: { paddingHorizontal: spacing.md, paddingTop: spacing.md, paddingBottom: spacing.xxl, gap: spacing.xl },
  header: { minHeight: 60, flexDirection: 'row', alignItems: 'flex-start', justifyContent: 'space-between' },
  title: { color: colors.paper, fontSize: 30, lineHeight: 35, fontWeight: '800', letterSpacing: -0.8 },
  subtitle: { color: colors.muted, marginTop: 2, fontSize: 13 },
  demoBadge: { flexDirection: 'row', alignItems: 'center', gap: 6, paddingHorizontal: 9, paddingVertical: 6, borderRadius: 999, backgroundColor: colors.panel },
  demoDot: { width: 6, height: 6, borderRadius: 3, backgroundColor: colors.orange },
  demoText: { color: colors.muted, fontFamily: fonts.mono, fontSize: 7, fontWeight: '800', letterSpacing: 0.6 },
  section: { gap: spacing.sm },
  sectionHeadingRow: { flexDirection: 'row', alignItems: 'center', justifyContent: 'space-between' },
  sectionTitle: { color: colors.paper, fontSize: 16, fontWeight: '800' },
  historyCount: { color: colors.muted, fontFamily: fonts.mono, fontSize: 9 },
  list: { overflow: 'hidden', borderRadius: radius.md, backgroundColor: colors.panel },
  matchRow: { minHeight: 78, paddingHorizontal: 12, paddingVertical: 12, flexDirection: 'row', alignItems: 'center', gap: 12, borderBottomWidth: StyleSheet.hairlineWidth, borderBottomColor: colors.line },
  pressed: { opacity: 0.72 },
  avatar: { width: 46, height: 46, borderRadius: 23, alignItems: 'center', justifyContent: 'center', backgroundColor: colors.blue },
  avatarText: { color: colors.paper, fontSize: 15, fontWeight: '900' },
  matchCopy: { flex: 1, minWidth: 0 },
  statusLine: { flexDirection: 'row', alignItems: 'center', gap: 5 },
  statusDot: { width: 6, height: 6, borderRadius: 3 },
  status: { fontFamily: fonts.mono, fontSize: 8, fontWeight: '900', textTransform: 'uppercase' },
  code: { color: colors.muted, marginLeft: 'auto', fontFamily: fonts.mono, fontSize: 8 },
  opponent: { color: colors.paper, marginTop: 4, fontSize: 15, fontWeight: '800' },
  meta: { color: colors.muted, marginTop: 2, fontSize: 10 },
  historyRow: { minHeight: 66, flexDirection: 'row', alignItems: 'center', gap: 11, borderBottomWidth: StyleSheet.hairlineWidth, borderBottomColor: colors.line },
  form: { width: 28, height: 28, borderRadius: 14, alignItems: 'center', justifyContent: 'center' },
  win: { backgroundColor: colors.acid },
  loss: { backgroundColor: colors.orange },
  formText: { color: colors.ink, fontFamily: fonts.mono, fontSize: 10, fontWeight: '900' },
  historyCopy: { flex: 1, minWidth: 0 },
  historyOpponent: { color: colors.paper, fontSize: 13, fontWeight: '700' },
  historyMeta: { color: colors.muted, marginTop: 2, fontSize: 10 },
  scoreCopy: { alignItems: 'flex-end' },
  historyScore: { color: colors.paper, fontFamily: fonts.mono, fontSize: 13, fontWeight: '800' },
  ratingDelta: { marginTop: 2, fontFamily: fonts.mono, fontSize: 9, fontWeight: '700' },
});
