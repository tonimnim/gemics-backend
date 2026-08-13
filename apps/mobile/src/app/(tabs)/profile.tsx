import { Ionicons } from '@expo/vector-icons';
import { ScrollView, StyleSheet, Text, View } from 'react-native';
import { SafeAreaView } from 'react-native-safe-area-context';

import { colors, fonts, radius, spacing } from '@/design/tokens';
import { PlayerAvatar } from '@/features/players/components';

const recentMatches = [
  { id: 'match-41', opponent: 'slick.ken', competition: 'Nairobi Open', score: '3–1', result: 'W' },
  { id: 'match-40', opponent: 'reign254', competition: 'Sunday Rivals', score: '1–2', result: 'L' },
  { id: 'match-39', opponent: 'musa.efc', competition: 'Sunday Rivals', score: '2–0', result: 'W' },
];

const tournamentHistory = [
  { id: 'event-12', name: 'Nairobi Open', detail: 'Quarter-final · In progress', placement: 'Top 8' },
  { id: 'event-11', name: 'Sunday Rivals', detail: '32 players · 28 Jul', placement: '2nd' },
  { id: 'event-10', name: 'Weekend Cup', detail: '64 players · 20 Jul', placement: 'Top 16' },
];

export default function ProfileScreen() {
  return (
    <SafeAreaView style={styles.safeArea} edges={['top']}>
      <ScrollView contentContainerStyle={styles.content} showsVerticalScrollIndicator={false}>
        <View style={styles.screenHeader}>
          <Text style={styles.screenTitle}>Profile</Text>
        </View>

        <View style={styles.identity}>
          <View style={styles.avatarFrame}>
            <PlayerAvatar accessible name="Brian Maina" size={78} uri="https://i.pravatar.cc/200?img=12" />
          </View>
          <View style={styles.identityCopy}>
            <View style={styles.nameRow}>
              <Text numberOfLines={1} style={styles.name}>Brian Maina</Text>
              <Ionicons color={colors.blue} name="checkmark-circle" size={18} />
            </View>
            <Text style={styles.handle}>@brian.mainaa</Text>
            <Text style={styles.game}>eFootball Mobile · Nairobi</Text>
          </View>
        </View>

        <View style={styles.performance}>
          <View style={styles.performanceItem}>
            <Text style={styles.performanceValue}>1,842</Text>
            <Text style={styles.performanceLabel}>RATING</Text>
          </View>
          <View style={styles.divider} />
          <View style={styles.performanceItem}>
            <Text style={styles.performanceValue}>#24</Text>
            <Text style={styles.performanceLabel}>KENYA RANK</Text>
          </View>
          <View style={styles.divider} />
          <View style={styles.performanceItem}>
            <Text style={styles.performanceValue}>78%</Text>
            <Text style={styles.performanceLabel}>WIN RATE</Text>
          </View>
        </View>

        <View style={styles.section}>
          <View style={styles.sectionHeading}>
            <Text style={styles.sectionTitle}>Recent matches</Text>
          </View>
          <View style={styles.list}>
            {recentMatches.map((match, index) => (
              <View key={match.id} style={[styles.row, index < recentMatches.length - 1 && styles.rowBorder]}>
                <View style={[styles.result, match.result === 'L' && styles.resultLoss]}>
                  <Text style={styles.resultText}>{match.result}</Text>
                </View>
                <View style={styles.rowCopy}>
                  <Text style={styles.rowTitle}>vs {match.opponent}</Text>
                  <Text numberOfLines={1} style={styles.rowDetail}>{match.competition}</Text>
                </View>
                <Text style={styles.score}>{match.score}</Text>
              </View>
            ))}
          </View>
        </View>

        <View style={styles.section}>
          <View style={styles.sectionHeading}>
            <Text style={styles.sectionTitle}>Tournament history</Text>
          </View>
          <View style={styles.list}>
            {tournamentHistory.map((tournament, index) => (
              <View key={tournament.id} style={[styles.row, index < tournamentHistory.length - 1 && styles.rowBorder]}>
                <View style={styles.tournamentIcon}>
                  <Ionicons color={colors.acid} name="trophy-outline" size={19} />
                </View>
                <View style={styles.rowCopy}>
                  <Text style={styles.rowTitle}>{tournament.name}</Text>
                  <Text numberOfLines={1} style={styles.rowDetail}>{tournament.detail}</Text>
                </View>
                <Text style={styles.placement}>{tournament.placement}</Text>
              </View>
            ))}
          </View>
        </View>

        <View style={styles.accountRow}>
          <View>
            <Text style={styles.accountLabel}>CONNECTED GAME ACCOUNT</Text>
            <Text style={styles.accountValue}>eFootball · brian_mainaa</Text>
          </View>
          <View style={styles.pendingPill}><Text style={styles.pendingText}>PENDING</Text></View>
        </View>
      </ScrollView>
    </SafeAreaView>
  );
}

const styles = StyleSheet.create({
  safeArea: { flex: 1, backgroundColor: colors.ink },
  content: { paddingHorizontal: spacing.lg, paddingTop: spacing.sm, paddingBottom: spacing.xxl, gap: spacing.xl },
  screenHeader: { minHeight: 52, flexDirection: 'row', alignItems: 'center' },
  screenTitle: { color: colors.paper, fontSize: 28, fontWeight: '900', letterSpacing: -0.8 },
  identity: { flexDirection: 'row', alignItems: 'center' },
  avatarFrame: { width: 82, height: 82, borderRadius: 41, alignItems: 'center', justifyContent: 'center', borderWidth: 2, borderColor: colors.acid },
  identityCopy: { flex: 1, minWidth: 0, marginLeft: spacing.md },
  nameRow: { flexDirection: 'row', alignItems: 'center', gap: 6 },
  name: { flexShrink: 1, color: colors.paper, fontSize: 22, fontWeight: '900', letterSpacing: -0.5 },
  handle: { color: colors.acid, marginTop: 3, fontFamily: fonts.mono, fontSize: 11, fontWeight: '700' },
  game: { color: colors.muted, marginTop: 7, fontSize: 12 },
  performance: { flexDirection: 'row', alignItems: 'center', paddingVertical: spacing.md, borderTopWidth: 1, borderBottomWidth: 1, borderColor: colors.line },
  performanceItem: { flex: 1, alignItems: 'center' },
  performanceValue: { color: colors.paper, fontSize: 19, fontWeight: '900' },
  performanceLabel: { color: colors.muted, marginTop: 3, fontFamily: fonts.mono, fontSize: 7, letterSpacing: 0.5 },
  divider: { width: 1, height: 32, backgroundColor: colors.line },
  section: { gap: spacing.md },
  sectionHeading: { flexDirection: 'row', alignItems: 'center', justifyContent: 'space-between' },
  sectionTitle: { color: colors.paper, fontSize: 18, fontWeight: '800' },
  list: { borderTopWidth: 1, borderBottomWidth: 1, borderColor: colors.line },
  row: { minHeight: 70, flexDirection: 'row', alignItems: 'center', gap: spacing.md },
  rowBorder: { borderBottomWidth: 1, borderBottomColor: colors.line },
  result: { width: 34, height: 34, borderRadius: 10, alignItems: 'center', justifyContent: 'center', backgroundColor: colors.acid },
  resultLoss: { backgroundColor: colors.orange },
  resultText: { color: colors.ink, fontFamily: fonts.mono, fontSize: 11, fontWeight: '900' },
  tournamentIcon: { width: 34, height: 34, borderRadius: 10, alignItems: 'center', justifyContent: 'center', backgroundColor: colors.panel },
  rowCopy: { flex: 1, minWidth: 0 },
  rowTitle: { color: colors.paper, fontSize: 13, fontWeight: '800' },
  rowDetail: { color: colors.muted, marginTop: 3, fontSize: 11 },
  score: { color: colors.paper, fontFamily: fonts.mono, fontSize: 13, fontWeight: '900' },
  placement: { color: colors.acid, fontFamily: fonts.mono, fontSize: 10, fontWeight: '800' },
  accountRow: { padding: spacing.md, flexDirection: 'row', alignItems: 'center', justifyContent: 'space-between', borderRadius: radius.md, backgroundColor: colors.panel, borderWidth: 1, borderColor: colors.line },
  accountLabel: { color: colors.muted, fontFamily: fonts.mono, fontSize: 7, letterSpacing: 0.6 },
  accountValue: { color: colors.paper, marginTop: 5, fontSize: 12, fontWeight: '700' },
  pendingPill: { paddingHorizontal: spacing.sm, paddingVertical: 5, borderRadius: 10, backgroundColor: 'rgba(255,116,72,0.16)' },
  pendingText: { color: colors.orange, fontFamily: fonts.mono, fontSize: 7, fontWeight: '900' },
});
