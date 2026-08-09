import { Link } from 'expo-router';
import { Pressable, ScrollView, StyleSheet, Text, View } from 'react-native';
import { SafeAreaView } from 'react-native-safe-area-context';

import { BrandMark, CompetitionCard, Eyebrow, Stat } from '@/components/product';
import { featuredCompetitions } from '@/features/competitions/demo';
import { colors, fonts, radius, spacing } from '@/design/tokens';

export default function ArenaScreen() {
  return (
    <SafeAreaView style={styles.safeArea} edges={['top']}>
      <ScrollView contentContainerStyle={styles.content} showsVerticalScrollIndicator={false}>
        <View style={styles.topbar}>
          <BrandMark />
          <View style={styles.greeting}><Text style={styles.kicker}>GOOD EVENING</Text><Text style={styles.name}>Brian.</Text></View>
          <Pressable accessibilityLabel="Notifications" style={styles.notification}><Text style={styles.notificationText}>●</Text></Pressable>
        </View>

        <View style={styles.heroCard}>
          <View style={styles.heroStripe} />
          <View style={styles.heroHeader}><Eyebrow label="NEXT MATCH" /><Text style={styles.timer}>01:42:18</Text></View>
          <Text style={styles.heroTitle}>ROUND 2</Text>
          <View style={styles.versusRow}>
            <View style={styles.competitor}><View style={styles.avatar}><Text style={styles.avatarText}>BM</Text></View><Text style={styles.handle}>YOU</Text></View>
            <View style={styles.versus}><Text style={styles.versusText}>VS</Text><Text style={styles.matchCode}>M-024</Text></View>
            <View style={styles.competitor}><View style={[styles.avatar, styles.awayAvatar]}><Text style={styles.avatarText}>SK</Text></View><Text style={styles.handle}>SLICK.KEN</Text></View>
          </View>
          <Pressable style={styles.checkInButton}><Text style={styles.checkInText}>CHECK IN NOW</Text><Text style={styles.checkInArrow}>↗</Text></Pressable>
        </View>

        <View style={styles.sectionHeader}>
          <View><Eyebrow label="OPEN NOW" /><Text style={styles.sectionTitle}>STEP INTO{`\n`}THE BRACKET.</Text></View>
          <Link href="/(tabs)/competitions" style={styles.viewAll}>VIEW ALL →</Link>
        </View>

        <View style={styles.cards}>
          {featuredCompetitions.slice(0, 2).map((competition) => <CompetitionCard competition={competition} key={competition.id} />)}
        </View>

        <View style={styles.recordCard}>
          <View style={styles.recordTitleRow}><View><Text style={styles.recordLabel}>YOUR SEASON</Text><Text style={styles.recordTitle}>FORM CHECK</Text></View><Text style={styles.rank}>#024 KE</Text></View>
          <View style={styles.stats}><Stat label="WIN RATE" value="78%" /><Stat label="MATCHES" value="41" /><Stat label="RATING" value="1,842" /></View>
          <View style={styles.formRow}>{['W', 'W', 'L', 'W', 'W'].map((form, index) => <View key={`${form}-${index}`} style={[styles.form, form === 'L' && styles.loss]}><Text style={styles.formText}>{form}</Text></View>)}</View>
        </View>
      </ScrollView>
    </SafeAreaView>
  );
}

const styles = StyleSheet.create({
  safeArea: { flex: 1, backgroundColor: colors.ink },
  content: { paddingHorizontal: spacing.lg, paddingBottom: spacing.xxl, gap: spacing.xl },
  topbar: { minHeight: 74, flexDirection: 'row', alignItems: 'center', gap: spacing.md },
  greeting: { flex: 1 }, kicker: { color: colors.muted, fontFamily: fonts.mono, fontSize: 9, letterSpacing: 1.2 }, name: { color: colors.paper, fontSize: 18, fontWeight: '800' },
  notification: { width: 42, height: 42, borderRadius: 21, alignItems: 'center', justifyContent: 'center', borderWidth: 1, borderColor: colors.line },
  notificationText: { color: colors.orange, fontSize: 11 },
  heroCard: { overflow: 'hidden', backgroundColor: colors.paper, padding: spacing.lg, borderRadius: radius.lg }, heroStripe: { position: 'absolute', top: 0, right: 0, width: 80, height: 12, backgroundColor: colors.acid },
  heroHeader: { flexDirection: 'row', justifyContent: 'space-between', alignItems: 'center' }, timer: { color: colors.ink, fontFamily: fonts.mono, fontSize: 10, fontWeight: '800' },
  heroTitle: { color: colors.ink, marginTop: spacing.sm, fontSize: 42, lineHeight: 44, fontWeight: '900', letterSpacing: -2 },
  versusRow: { flexDirection: 'row', alignItems: 'center', justifyContent: 'space-between', marginVertical: spacing.lg }, competitor: { width: 96, alignItems: 'center', gap: spacing.sm },
  avatar: { width: 68, height: 68, alignItems: 'center', justifyContent: 'center', backgroundColor: colors.blue, borderWidth: 2, borderColor: colors.ink, transform: [{ rotate: '-3deg' }] }, awayAvatar: { backgroundColor: colors.orange, transform: [{ rotate: '3deg' }] },
  avatarText: { color: colors.paper, fontSize: 23, fontWeight: '900' }, handle: { color: colors.ink, fontFamily: fonts.mono, fontSize: 9, fontWeight: '800' }, versus: { alignItems: 'center', gap: 3 }, versusText: { color: colors.ink, fontSize: 24, fontWeight: '900' }, matchCode: { color: colors.subtleInk, fontFamily: fonts.mono, fontSize: 8 },
  checkInButton: { minHeight: 52, paddingHorizontal: spacing.md, flexDirection: 'row', alignItems: 'center', justifyContent: 'space-between', backgroundColor: colors.acid, borderWidth: 2, borderColor: colors.ink },
  checkInText: { color: colors.ink, fontFamily: fonts.mono, fontWeight: '900', fontSize: 11 }, checkInArrow: { color: colors.ink, fontSize: 17, fontWeight: '900' },
  sectionHeader: { flexDirection: 'row', justifyContent: 'space-between', alignItems: 'flex-end' }, sectionTitle: { color: colors.paper, fontSize: 31, lineHeight: 29, letterSpacing: -1.5, fontWeight: '900' }, viewAll: { color: colors.acid, fontFamily: fonts.mono, fontSize: 9, fontWeight: '800', paddingBottom: 3 },
  cards: { gap: spacing.md },
  recordCard: { backgroundColor: colors.panel, borderWidth: 1, borderColor: colors.line, borderRadius: radius.lg, padding: spacing.lg }, recordTitleRow: { flexDirection: 'row', justifyContent: 'space-between' }, recordLabel: { color: colors.muted, fontFamily: fonts.mono, fontSize: 8, letterSpacing: 1 }, recordTitle: { color: colors.paper, fontSize: 24, fontWeight: '900' }, rank: { color: colors.acid, fontFamily: fonts.mono, fontSize: 10, fontWeight: '800' },
  stats: { marginTop: spacing.lg, paddingVertical: spacing.md, flexDirection: 'row', borderTopWidth: 1, borderBottomWidth: 1, borderColor: colors.line },
  formRow: { flexDirection: 'row', gap: spacing.sm, marginTop: spacing.md }, form: { width: 27, height: 27, backgroundColor: colors.acid, alignItems: 'center', justifyContent: 'center' }, loss: { backgroundColor: colors.orange }, formText: { color: colors.ink, fontFamily: fonts.mono, fontSize: 10, fontWeight: '900' },
});
