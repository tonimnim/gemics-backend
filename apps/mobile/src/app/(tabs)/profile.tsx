import { ScrollView, StyleSheet, Text, View } from 'react-native';
import { SafeAreaView } from 'react-native-safe-area-context';

import { BrandMark, Stat } from '@/components/product';
import { colors, fonts, radius, spacing } from '@/design/tokens';

export default function ProfileScreen() {
  return (
    <SafeAreaView style={styles.safeArea} edges={['top']}>
      <ScrollView contentContainerStyle={styles.content}>
        <View style={styles.top}><BrandMark /><Text style={styles.eyebrow}>PLAYER CARD // KE</Text></View>
        <View style={styles.playerCard}>
          <View style={styles.art}><Text style={styles.initials}>BM</Text><Text style={styles.rank}>#024 KENYA</Text></View>
          <Text style={styles.name}>BRIAN{`\n`}MAINA</Text><Text style={styles.handle}>@brian.mainaa</Text>
          <View style={styles.stats}><Stat value="78%" label="WIN RATE" dark /><Stat value="41" label="MATCHES" dark /><Stat value="1,842" label="RATING" dark /></View>
        </View>
        <Text style={styles.section}>COMPETITIVE IDENTITY</Text>
        <View style={styles.details}><View><Text style={styles.label}>PRIMARY GAME</Text><Text style={styles.value}>eFootball Mobile</Text></View><View><Text style={styles.label}>REGION</Text><Text style={styles.value}>Nairobi, Kenya</Text></View><View><Text style={styles.label}>GAME ACCOUNT</Text><Text style={styles.value}>Pending verification</Text></View></View>
      </ScrollView>
    </SafeAreaView>
  );
}

const styles = StyleSheet.create({
  safeArea: { flex: 1, backgroundColor: colors.ink }, content: { padding: spacing.lg, paddingBottom: spacing.xxl }, top: { flexDirection: 'row', alignItems: 'center', justifyContent: 'space-between', marginBottom: spacing.xl }, eyebrow: { color: colors.muted, fontFamily: fonts.mono, fontSize: 8 }, playerCard: { padding: spacing.lg, backgroundColor: colors.acid, borderRadius: radius.lg, transform: [{ rotate: '-1deg' }] }, art: { height: 205, alignItems: 'center', justifyContent: 'center', backgroundColor: colors.blue, overflow: 'hidden' }, initials: { color: colors.paper, fontSize: 96, fontWeight: '900', letterSpacing: -8 }, rank: { position: 'absolute', right: 12, bottom: 12, padding: 8, color: colors.ink, backgroundColor: colors.acid, fontFamily: fonts.mono, fontSize: 9, fontWeight: '900' }, name: { color: colors.ink, marginTop: spacing.md, fontSize: 38, lineHeight: 34, fontWeight: '900', letterSpacing: -2 }, handle: { color: colors.subtleInk, marginTop: 4, fontFamily: fonts.mono, fontSize: 10 }, stats: { flexDirection: 'row', marginTop: spacing.lg, paddingTop: spacing.md, borderTopWidth: 1, borderColor: colors.ink }, section: { color: colors.muted, marginTop: spacing.xl, marginBottom: spacing.md, fontFamily: fonts.mono, fontSize: 9, letterSpacing: 1 }, details: { gap: spacing.md, padding: spacing.lg, borderRadius: radius.lg, backgroundColor: colors.panel, borderWidth: 1, borderColor: colors.line }, label: { color: colors.muted, fontFamily: fonts.mono, fontSize: 8 }, value: { color: colors.paper, marginTop: 3, fontSize: 14, fontWeight: '700' },
});
