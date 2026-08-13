import { Ionicons } from '@expo/vector-icons';
import { Link } from 'expo-router';
import { Pressable, ScrollView, StyleSheet, Text, View } from 'react-native';
import { SafeAreaView } from 'react-native-safe-area-context';

import { CompetitionCard, Eyebrow, Stat } from '@/components/product';
import { colors, fonts, radius, spacing } from '@/design/tokens';
import { featuredCompetitions } from '@/features/competitions/demo';
import { PlayerAvatar } from '@/features/players/components';

const player = {
  name: 'Brian',
  avatarUrl: 'https://i.pravatar.cc/160?img=12',
};

export default function ArenaScreen() {
  return (
    <SafeAreaView style={styles.safeArea} edges={['top']}>
      <ScrollView contentContainerStyle={styles.content} showsVerticalScrollIndicator={false}>
        <View style={styles.topbar}>
          <Link href="/(tabs)/profile" asChild>
            <Pressable accessibilityLabel="Open my profile" hitSlop={8}>
              <PlayerAvatar accessible name={player.name} size={48} uri={player.avatarUrl} />
            </Pressable>
          </Link>
          <View style={styles.greeting}>
            <Text style={styles.welcome}>Welcome back</Text>
            <Text style={styles.playerName}>{player.name}</Text>
          </View>
        </View>

        <View style={styles.nextMatch}>
          <View style={styles.matchTopline}>
            <Eyebrow label="NEXT MATCH" />
            <View style={styles.livePill}>
              <View style={styles.liveDot} />
              <Text style={styles.liveText}>CHECK-IN OPEN</Text>
            </View>
          </View>
          <View style={styles.roundRow}>
            <Text style={styles.round}>Round 2</Text>
            <Text style={styles.timer}>01:42:18</Text>
          </View>
          <View style={styles.versusRow}>
            <View style={styles.competitor}>
              <PlayerAvatar name="Brian Maina" size={36} uri={player.avatarUrl} />
              <View style={styles.competitorCopy}>
                <Text style={styles.sideLabel}>YOU</Text>
                <Text numberOfLines={1} style={styles.handle}>brian.mainaa</Text>
              </View>
            </View>
            <Text style={styles.versus}>VS</Text>
            <View style={[styles.competitor, styles.competitorAway]}>
              <View style={styles.competitorCopy}>
                <Text style={[styles.sideLabel, styles.alignRight]}>OPPONENT</Text>
                <Text numberOfLines={1} style={[styles.handle, styles.alignRight]}>slick.ken</Text>
              </View>
              <View style={[styles.matchAvatar, styles.awayAvatar]}>
                <Text style={styles.awayInitials}>SK</Text>
              </View>
            </View>
          </View>
          <Link href="/match/match-024" asChild>
            <Pressable accessibilityRole="button" style={styles.primaryButton}>
              <Text style={styles.primaryButtonText}>Open match room</Text>
              <Ionicons color={colors.ink} name="arrow-forward" size={20} />
            </Pressable>
          </Link>
        </View>

        <View style={styles.sectionHeader}>
          <Text style={styles.sectionTitle}>Open competitions</Text>
          <Link href="/(tabs)/competitions" style={styles.viewAll}>See all</Link>
        </View>
        <View style={styles.competitionList}>
          {featuredCompetitions.slice(0, 2).map((competition) => (
            <CompetitionCard competition={competition} key={competition.id} />
          ))}
        </View>

        <View style={styles.seasonSummary}>
          <View style={styles.summaryHeading}>
            <Text style={styles.summaryTitle}>Your form</Text>
            <Text style={styles.rank}>#24 Kenya</Text>
          </View>
          <View style={styles.stats}>
            <Stat label="WIN RATE" value="78%" />
            <Stat label="MATCHES" value="41" />
            <Stat label="RATING" value="1,842" />
          </View>
          <View style={styles.formRow}>
            {['W', 'W', 'L', 'W', 'W'].map((form, index) => (
              <View key={`${form}-${index}`} style={[styles.form, form === 'L' && styles.loss]}>
                <Text style={styles.formText}>{form}</Text>
              </View>
            ))}
          </View>
        </View>
      </ScrollView>
    </SafeAreaView>
  );
}

const styles = StyleSheet.create({
  safeArea: { flex: 1, backgroundColor: colors.ink },
  content: {
    paddingHorizontal: spacing.lg,
    paddingTop: spacing.sm,
    paddingBottom: spacing.xxl,
    gap: spacing.xl,
  },
  topbar: { minHeight: 58, flexDirection: 'row', alignItems: 'center' },
  greeting: { flex: 1, marginLeft: spacing.md },
  welcome: { color: colors.muted, fontSize: 12 },
  playerName: { color: colors.paper, marginTop: 1, fontSize: 19, fontWeight: '800' },
  nextMatch: { backgroundColor: colors.paper, padding: spacing.lg, borderRadius: radius.lg },
  matchTopline: { minHeight: 23, flexDirection: 'row', alignItems: 'flex-start', justifyContent: 'space-between' },
  livePill: { flexDirection: 'row', alignItems: 'center', gap: 5 },
  liveDot: { width: 6, height: 6, borderRadius: 3, backgroundColor: colors.green },
  liveText: { color: colors.subtleInk, fontFamily: fonts.mono, fontSize: 7, fontWeight: '800' },
  roundRow: { flexDirection: 'row', alignItems: 'center', justifyContent: 'space-between', marginTop: spacing.xs },
  round: { color: colors.ink, fontSize: 28, fontWeight: '900', letterSpacing: -1 },
  timer: { color: colors.subtleInk, fontFamily: fonts.mono, fontSize: 10, fontWeight: '800' },
  versusRow: { flexDirection: 'row', alignItems: 'center', marginVertical: spacing.lg },
  competitor: { flex: 1, minWidth: 0, flexDirection: 'row', alignItems: 'center', gap: 6 },
  competitorAway: { justifyContent: 'flex-end' },
  competitorCopy: { flex: 1, minWidth: 0 },
  matchAvatar: { width: 36, height: 36, borderRadius: 18, backgroundColor: colors.blue },
  awayAvatar: { alignItems: 'center', justifyContent: 'center', backgroundColor: colors.orange },
  awayInitials: { color: colors.paper, fontSize: 14, fontWeight: '900' },
  sideLabel: { color: colors.subtleInk, fontFamily: fonts.mono, fontSize: 7, fontWeight: '800' },
  handle: { color: colors.ink, marginTop: 2, fontSize: 11, fontWeight: '800' },
  alignRight: { textAlign: 'right' },
  versus: { color: colors.subtleInk, marginHorizontal: 4, fontFamily: fonts.mono, fontSize: 10, fontWeight: '900' },
  primaryButton: {
    minHeight: 50,
    paddingHorizontal: spacing.md,
    flexDirection: 'row',
    alignItems: 'center',
    justifyContent: 'space-between',
    backgroundColor: colors.acid,
    borderRadius: radius.md,
  },
  primaryButtonText: { color: colors.ink, fontSize: 14, fontWeight: '900' },
  sectionHeader: { flexDirection: 'row', alignItems: 'center', justifyContent: 'space-between' },
  sectionTitle: { color: colors.paper, fontSize: 20, fontWeight: '800' },
  viewAll: { color: colors.acid, fontSize: 12, fontWeight: '800' },
  competitionList: { gap: spacing.md },
  seasonSummary: {
    padding: spacing.lg,
    borderRadius: radius.lg,
    backgroundColor: colors.panel,
    borderWidth: 1,
    borderColor: colors.line,
  },
  summaryHeading: { flexDirection: 'row', alignItems: 'center', justifyContent: 'space-between' },
  summaryTitle: { color: colors.paper, fontSize: 19, fontWeight: '800' },
  rank: { color: colors.acid, fontFamily: fonts.mono, fontSize: 10, fontWeight: '800' },
  stats: { flexDirection: 'row', marginTop: spacing.lg, paddingTop: spacing.md, borderTopWidth: 1, borderColor: colors.line },
  formRow: { flexDirection: 'row', gap: spacing.sm, marginTop: spacing.md },
  form: { width: 30, height: 30, borderRadius: 8, backgroundColor: colors.acid, alignItems: 'center', justifyContent: 'center' },
  loss: { backgroundColor: colors.orange },
  formText: { color: colors.ink, fontFamily: fonts.mono, fontSize: 10, fontWeight: '900' },
});
