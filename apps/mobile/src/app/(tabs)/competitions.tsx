import { ScrollView, StyleSheet, Text, View } from 'react-native';
import { SafeAreaView } from 'react-native-safe-area-context';

import { CompetitionCard, Eyebrow } from '@/components/product';
import { featuredCompetitions } from '@/features/competitions/demo';
import { colors, fonts, spacing } from '@/design/tokens';

export default function CompetitionsScreen() {
  return (
    <SafeAreaView style={styles.safeArea} edges={['top']}>
      <ScrollView contentContainerStyle={styles.content} showsVerticalScrollIndicator={false}>
        <Eyebrow label="KENYA // MOBILE" />
        <Text style={styles.title}>FIND YOUR{`\n`}NEXT TEST.</Text>
        <Text style={styles.body}>Verified eFootball Mobile competitions for players ready to build a real competitive record.</Text>
        <View style={styles.filterRow}><View style={styles.activeFilter}><Text style={styles.activeFilterText}>OPEN</Text></View><Text style={styles.filter}>UPCOMING</Text><Text style={styles.filter}>MY EVENTS</Text></View>
        <View style={styles.list}>{featuredCompetitions.map((competition) => <CompetitionCard competition={competition} key={competition.id} />)}</View>
      </ScrollView>
    </SafeAreaView>
  );
}

const styles = StyleSheet.create({
  safeArea: { flex: 1, backgroundColor: colors.ink }, content: { padding: spacing.lg, paddingBottom: spacing.xxl }, title: { color: colors.paper, fontSize: 46, lineHeight: 42, letterSpacing: -2.4, fontWeight: '900' }, body: { maxWidth: 330, color: colors.muted, fontSize: 14, lineHeight: 21, marginTop: spacing.md },
  filterRow: { flexDirection: 'row', alignItems: 'center', gap: spacing.lg, marginVertical: spacing.xl }, activeFilter: { paddingHorizontal: 13, paddingVertical: 8, backgroundColor: colors.acid }, activeFilterText: { color: colors.ink, fontFamily: fonts.mono, fontSize: 9, fontWeight: '900' }, filter: { color: colors.muted, fontFamily: fonts.mono, fontSize: 9, fontWeight: '800' }, list: { gap: spacing.md },
});
