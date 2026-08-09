import { Pressable, ScrollView, StyleSheet, Text, View } from 'react-native';
import { SafeAreaView } from 'react-native-safe-area-context';

import { Eyebrow } from '@/components/product';
import { colors, fonts, radius, spacing } from '@/design/tokens';

export default function MatchesScreen() {
  return (
    <SafeAreaView style={styles.safeArea} edges={['top']}>
      <ScrollView contentContainerStyle={styles.content}>
        <Eyebrow label="MATCH ROOM" />
        <Text style={styles.title}>PLAY.{`\n`}PROVE IT.</Text>
        <View style={styles.activeCard}>
          <View style={styles.statusRow}><Text style={styles.live}>● READY FOR CHECK-IN</Text><Text style={styles.code}>M-024</Text></View>
          <Text style={styles.event}>Nairobi Sunday Knockout</Text>
          <View style={styles.scoreRow}><Text style={styles.player}>YOU</Text><Text style={styles.score}>— : —</Text><Text style={styles.player}>SLICK.KEN</Text></View>
          <Pressable style={styles.primary}><Text style={styles.primaryText}>OPEN MATCH ROOM</Text></Pressable>
        </View>
        <Text style={styles.section}>RECENT</Text>
        {[['W', '3 – 1', '@tiki_taka254'], ['W', '2 – 0', '@mombasa10'], ['L', '1 – 2', '@pressingking']].map(([form, score, opponent]) => (
          <View style={styles.history} key={opponent}><View style={[styles.form, form === 'L' && styles.loss]}><Text style={styles.formText}>{form}</Text></View><View style={styles.historyCopy}><Text style={styles.opponent}>{opponent}</Text><Text style={styles.meta}>eFootball Mobile • Confirmed</Text></View><Text style={styles.historyScore}>{score}</Text></View>
        ))}
      </ScrollView>
    </SafeAreaView>
  );
}

const styles = StyleSheet.create({
  safeArea: { flex: 1, backgroundColor: colors.ink }, content: { padding: spacing.lg, paddingBottom: spacing.xxl }, title: { color: colors.paper, fontSize: 50, lineHeight: 45, letterSpacing: -2.5, fontWeight: '900' }, activeCard: { marginTop: spacing.xl, padding: spacing.lg, borderRadius: radius.lg, backgroundColor: colors.paper }, statusRow: { flexDirection: 'row', justifyContent: 'space-between' }, live: { color: colors.green, fontFamily: fonts.mono, fontSize: 8, fontWeight: '900' }, code: { color: colors.subtleInk, fontFamily: fonts.mono, fontSize: 8 }, event: { color: colors.ink, fontSize: 18, fontWeight: '800', marginTop: spacing.md }, scoreRow: { flexDirection: 'row', justifyContent: 'space-between', alignItems: 'center', marginVertical: spacing.xl }, player: { width: 82, color: colors.ink, fontFamily: fonts.mono, fontSize: 9, fontWeight: '800', textAlign: 'center' }, score: { color: colors.ink, fontSize: 30, fontWeight: '900' }, primary: { minHeight: 52, alignItems: 'center', justifyContent: 'center', backgroundColor: colors.acid, borderWidth: 2, borderColor: colors.ink }, primaryText: { color: colors.ink, fontFamily: fonts.mono, fontSize: 10, fontWeight: '900' }, section: { marginTop: spacing.xl, marginBottom: spacing.md, color: colors.muted, fontFamily: fonts.mono, fontSize: 9, letterSpacing: 1 }, history: { flexDirection: 'row', alignItems: 'center', gap: spacing.md, minHeight: 72, borderBottomWidth: 1, borderColor: colors.line }, form: { width: 28, height: 28, alignItems: 'center', justifyContent: 'center', backgroundColor: colors.acid }, loss: { backgroundColor: colors.orange }, formText: { color: colors.ink, fontFamily: fonts.mono, fontSize: 10, fontWeight: '900' }, historyCopy: { flex: 1 }, opponent: { color: colors.paper, fontSize: 14, fontWeight: '800' }, meta: { color: colors.muted, fontSize: 10, marginTop: 2 }, historyScore: { color: colors.paper, fontFamily: fonts.mono, fontSize: 13, fontWeight: '800' },
});
