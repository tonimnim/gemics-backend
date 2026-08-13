import { router, useLocalSearchParams } from 'expo-router';
import { Pressable, ScrollView, StyleSheet, Text, View } from 'react-native';
import { SafeAreaView } from 'react-native-safe-area-context';

import { colors, fonts, spacing } from '@/design/tokens';
import { Movement, PlayerAvatar } from '@/features/players/components';
import { findPlayer } from '@/features/players/demo';
import { PlayerForm } from '@/features/players/types';

function FormMark({ result }: { result: PlayerForm }) {
  return (
    <View style={[styles.formMark, result === 'L' && styles.formLoss, result === 'D' && styles.formDraw]}>
      <Text style={styles.formMarkText}>{result}</Text>
    </View>
  );
}

export default function PlayerDetailsScreen() {
  const { id } = useLocalSearchParams<{ id: string }>();
  const player = findPlayer(Array.isArray(id) ? id[0] : id);

  if (!player) {
    return (
      <SafeAreaView style={styles.safeArea}>
        <View style={styles.missingTopbar}>
          <Pressable accessibilityLabel="Go back" accessibilityRole="button" hitSlop={12} onPress={() => router.back()} style={styles.backButton}>
            <Text style={styles.backIcon}>‹</Text>
          </Pressable>
        </View>
        <View style={styles.missing}>
          <Text style={styles.missingTitle}>Player unavailable</Text>
          <Text style={styles.missingBody}>This profile may have moved or no longer be public.</Text>
        </View>
      </SafeAreaView>
    );
  }

  const played = player.wins + player.losses + player.draws;

  return (
    <SafeAreaView edges={['top']} style={styles.safeArea}>
      <ScrollView contentContainerStyle={styles.content} showsVerticalScrollIndicator={false}>
        <View style={styles.topbar}>
          <Pressable accessibilityLabel="Go back" accessibilityRole="button" hitSlop={12} onPress={() => router.back()} style={styles.backButton}>
            <Text style={styles.backIcon}>‹</Text>
          </Pressable>
          <Text style={styles.topbarTitle}>Player profile</Text>
          <View style={styles.topbarSpacer} />
        </View>

        <View style={styles.identityBlock}>
          <PlayerAvatar accessible name={player.name} size={94} uri={player.avatarUrl} />
          <View style={styles.identityCopy}>
            <Text style={styles.name}>{player.name}</Text>
            <Text style={styles.handle}>{player.handle}</Text>
            <Text style={styles.location}>{player.county}, Kenya · eFootball Mobile</Text>
          </View>
        </View>

        <View style={styles.ratingRow}>
          <View style={styles.ratingMain}>
            <Text style={styles.ratingValue}>{player.rating.toLocaleString()}</Text>
            <Text style={styles.metricLabel}>PLAYER RATING</Text>
          </View>
          <View style={styles.movementWrap}>
            <Movement value={player.rankMovement} />
            <Text style={styles.movementCaption}>THIS WEEK</Text>
          </View>
        </View>

        <View style={styles.metrics}>
          <View style={styles.metric}>
            <Text style={styles.metricValue}>#{player.nationalRank}</Text>
            <Text style={styles.metricLabel}>KENYA</Text>
          </View>
          <View style={styles.metric}>
            <Text style={styles.metricValue}>#{player.globalRank}</Text>
            <Text style={styles.metricLabel}>GLOBAL</Text>
          </View>
          <View style={styles.metric}>
            <Text style={styles.metricValue}>{player.winRate}%</Text>
            <Text style={styles.metricLabel}>WIN RATE</Text>
          </View>
          <View style={[styles.metric, styles.metricLast]}>
            <Text style={styles.metricValue}>{played}</Text>
            <Text style={styles.metricLabel}>MATCHES</Text>
          </View>
        </View>

        <View style={styles.formSection}>
          <View>
            <Text style={styles.sectionEyebrow}>RECENT FORM</Text>
            <Text style={styles.record}>{player.wins}W · {player.losses}L · {player.draws}D</Text>
          </View>
          <View style={styles.formRow}>
            {player.form.map((result, index) => <FormMark key={`${result}-${index}`} result={result} />)}
          </View>
        </View>

        <View style={styles.sectionHeader}>
          <Text style={styles.sectionTitle}>Match history</Text>
          <Text style={styles.sectionCount}>{player.matches.length} RECENT</Text>
        </View>
        <View style={styles.historyList}>
          {player.matches.map((match) => (
            <View key={match.id} style={styles.matchRow}>
              <FormMark result={match.result} />
              <View style={styles.historyCopy}>
                <Text numberOfLines={1} style={styles.historyTitle}>{match.opponentHandle}</Text>
                <Text numberOfLines={1} style={styles.historyMeta}>{match.competition} · {match.playedAt}</Text>
              </View>
              <View style={styles.matchResult}>
                <Text style={styles.score}>{match.score}</Text>
                <Text style={[styles.ratingDelta, match.ratingDelta < 0 && styles.negativeDelta]}>
                  {match.ratingDelta > 0 ? '+' : ''}{match.ratingDelta}
                </Text>
              </View>
            </View>
          ))}
        </View>

        <View style={styles.sectionHeader}>
          <Text style={styles.sectionTitle}>Tournament history</Text>
          <Text style={styles.sectionCount}>{player.tournaments.length} PLAYED</Text>
        </View>
        <View style={styles.historyList}>
          {player.tournaments.map((tournament) => (
            <View key={tournament.id} style={styles.tournamentRow}>
              <View style={styles.placement}>
                <Text style={styles.placementValue}>#{tournament.placement}</Text>
                <Text style={styles.placementLabel}>OF {tournament.entrants}</Text>
              </View>
              <View style={styles.historyCopy}>
                <Text numberOfLines={1} style={styles.historyTitle}>{tournament.name}</Text>
                <Text numberOfLines={1} style={styles.historyMeta}>{tournament.format} · {tournament.playedAt}</Text>
              </View>
              <Text style={styles.tournamentRecord}>{tournament.record}</Text>
            </View>
          ))}
        </View>

        <Text style={styles.memberSince}>Competitive record since {player.joinedAt}</Text>
      </ScrollView>
    </SafeAreaView>
  );
}

const styles = StyleSheet.create({
  safeArea: { flex: 1, backgroundColor: colors.ink },
  content: { paddingHorizontal: spacing.lg, paddingBottom: spacing.xxl },
  topbar: { height: 58, flexDirection: 'row', alignItems: 'center', justifyContent: 'space-between' },
  missingTopbar: { paddingHorizontal: spacing.lg, height: 58, justifyContent: 'center' },
  backButton: { width: 40, height: 40, alignItems: 'center', justifyContent: 'center', borderRadius: 20, borderWidth: 1, borderColor: colors.line },
  backIcon: { color: colors.paper, marginTop: -3, fontSize: 32, lineHeight: 34, fontWeight: '400' },
  topbarTitle: { color: colors.paper, fontSize: 14, fontWeight: '800' },
  topbarSpacer: { width: 40, height: 40 },
  identityBlock: { marginTop: spacing.md, flexDirection: 'row', alignItems: 'center', gap: spacing.md },
  identityCopy: { flex: 1 },
  name: { color: colors.paper, fontSize: 25, lineHeight: 29, fontWeight: '900', letterSpacing: -0.7 },
  handle: { color: colors.acid, marginTop: 3, fontSize: 12, fontWeight: '700' },
  location: { color: colors.muted, marginTop: spacing.sm, fontSize: 11, lineHeight: 16 },
  ratingRow: { marginTop: spacing.xl, paddingBottom: spacing.md, flexDirection: 'row', alignItems: 'flex-end', justifyContent: 'space-between', borderBottomWidth: 1, borderBottomColor: colors.line },
  ratingMain: { gap: 1 },
  ratingValue: { color: colors.paper, fontSize: 40, lineHeight: 44, fontWeight: '900', letterSpacing: -1.8 },
  movementWrap: { alignItems: 'flex-end', paddingBottom: 3 },
  movementCaption: { color: colors.muted, marginTop: 4, fontFamily: fonts.mono, fontSize: 6, letterSpacing: 0.6 },
  metrics: { minHeight: 86, flexDirection: 'row', alignItems: 'center', borderBottomWidth: 1, borderBottomColor: colors.line },
  metric: { flex: 1, alignItems: 'center', gap: 4, borderRightWidth: StyleSheet.hairlineWidth, borderRightColor: colors.line },
  metricLast: { borderRightWidth: 0 },
  metricValue: { color: colors.paper, fontFamily: fonts.mono, fontSize: 16, fontWeight: '900' },
  metricLabel: { color: colors.muted, fontFamily: fonts.mono, fontSize: 7, letterSpacing: 0.7 },
  formSection: { minHeight: 82, flexDirection: 'row', alignItems: 'center', justifyContent: 'space-between' },
  sectionEyebrow: { color: colors.muted, fontFamily: fonts.mono, fontSize: 7, letterSpacing: 0.8 },
  record: { color: colors.paper, marginTop: 5, fontSize: 12, fontWeight: '800' },
  formRow: { flexDirection: 'row', gap: 6 },
  formMark: { width: 27, height: 27, alignItems: 'center', justifyContent: 'center', borderRadius: 7, backgroundColor: colors.acid },
  formLoss: { backgroundColor: colors.orange },
  formDraw: { backgroundColor: colors.muted },
  formMarkText: { color: colors.ink, fontFamily: fonts.mono, fontSize: 9, fontWeight: '900' },
  sectionHeader: { marginTop: spacing.lg, marginBottom: spacing.sm, flexDirection: 'row', alignItems: 'baseline', justifyContent: 'space-between' },
  sectionTitle: { color: colors.paper, fontSize: 18, fontWeight: '900' },
  sectionCount: { color: colors.muted, fontFamily: fonts.mono, fontSize: 7, letterSpacing: 0.7 },
  historyList: { borderTopWidth: StyleSheet.hairlineWidth, borderTopColor: colors.line },
  matchRow: { minHeight: 68, flexDirection: 'row', alignItems: 'center', gap: 12, borderBottomWidth: StyleSheet.hairlineWidth, borderBottomColor: colors.line },
  tournamentRow: { minHeight: 76, flexDirection: 'row', alignItems: 'center', gap: 12, borderBottomWidth: StyleSheet.hairlineWidth, borderBottomColor: colors.line },
  historyCopy: { flex: 1, minWidth: 0 },
  historyTitle: { color: colors.paper, fontSize: 13, fontWeight: '800' },
  historyMeta: { color: colors.muted, marginTop: 4, fontSize: 9 },
  matchResult: { alignItems: 'flex-end', gap: 3 },
  score: { color: colors.paper, fontFamily: fonts.mono, fontSize: 13, fontWeight: '900' },
  ratingDelta: { color: colors.green, fontFamily: fonts.mono, fontSize: 8, fontWeight: '800' },
  negativeDelta: { color: colors.orange },
  placement: { width: 45 },
  placementValue: { color: colors.acid, fontFamily: fonts.mono, fontSize: 15, fontWeight: '900' },
  placementLabel: { color: colors.muted, marginTop: 3, fontFamily: fonts.mono, fontSize: 6 },
  tournamentRecord: { color: colors.paper, fontFamily: fonts.mono, fontSize: 9, fontWeight: '800' },
  memberSince: { color: colors.muted, marginTop: spacing.xl, textAlign: 'center', fontSize: 10 },
  missing: { flex: 1, alignItems: 'center', justifyContent: 'center', paddingHorizontal: spacing.xl },
  missingTitle: { color: colors.paper, fontSize: 21, fontWeight: '900' },
  missingBody: { color: colors.muted, marginTop: spacing.sm, fontSize: 13, lineHeight: 19, textAlign: 'center' },
});
