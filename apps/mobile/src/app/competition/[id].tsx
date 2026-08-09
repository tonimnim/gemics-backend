import { router, useLocalSearchParams } from 'expo-router';
import { Pressable, ScrollView, StyleSheet, Text, View } from 'react-native';
import { SafeAreaView } from 'react-native-safe-area-context';

import { Eyebrow, Stat } from '@/components/product';
import { featuredCompetitions } from '@/features/competitions/demo';
import { colors, fonts, radius, spacing } from '@/design/tokens';

export default function CompetitionDetailsScreen() {
  const { id } = useLocalSearchParams<{ id: string }>();
  const competition = featuredCompetitions.find((item) => item.id === id) ?? featuredCompetitions[0];
  return (
    <SafeAreaView style={styles.safeArea}>
      <ScrollView contentContainerStyle={styles.content}>
        <Pressable accessibilityLabel="Go back" onPress={() => router.back()}><Text style={styles.back}>← BACK</Text></Pressable>
        <Eyebrow label="eFOOTBALL MOBILE" />
        <Text style={styles.title}>{competition.name.toUpperCase()}</Text><Text style={styles.description}>{competition.description}</Text>
        <View style={styles.hero}><Text style={styles.number}>{competition.number}</Text><View style={styles.stats}><Stat value={competition.players} label="PLAYERS" dark /><Stat value="1v1" label="MODE" dark /><Stat value="FREE" label="ENTRY" dark /></View></View>
        <View style={styles.info}><Text style={styles.infoTitle}>WHAT TO EXPECT</Text><Text style={styles.infoBody}>Check in before your match, create a Friend Match in eFootball, then submit the final screen. Your opponent confirms the result or a referee reviews the evidence.</Text></View>
      </ScrollView>
      <View style={styles.bottom}><Pressable style={styles.register}><Text style={styles.registerText}>REGISTER FOR FREE</Text><Text style={styles.arrow}>↗</Text></Pressable></View>
    </SafeAreaView>
  );
}

const styles = StyleSheet.create({
  safeArea: { flex: 1, backgroundColor: colors.ink }, content: { padding: spacing.lg, paddingBottom: 130 }, back: { color: colors.paper, marginBottom: spacing.xl, fontFamily: fonts.mono, fontSize: 10, fontWeight: '800' }, title: { color: colors.paper, fontSize: 43, lineHeight: 40, fontWeight: '900', letterSpacing: -2 }, description: { color: colors.muted, marginTop: spacing.md, lineHeight: 21 }, hero: { marginTop: spacing.xl, padding: spacing.lg, borderRadius: radius.lg, backgroundColor: colors.acid }, number: { color: colors.ink, fontSize: 112, lineHeight: 112, fontWeight: '900', letterSpacing: -8 }, stats: { flexDirection: 'row', paddingTop: spacing.md, borderTopWidth: 2, borderColor: colors.ink }, info: { marginTop: spacing.lg, padding: spacing.lg, backgroundColor: colors.panel, borderRadius: radius.lg, borderWidth: 1, borderColor: colors.line }, infoTitle: { color: colors.paper, fontFamily: fonts.mono, fontSize: 10, fontWeight: '900' }, infoBody: { color: colors.muted, marginTop: spacing.sm, fontSize: 13, lineHeight: 20 }, bottom: { position: 'absolute', left: 0, right: 0, bottom: 0, padding: spacing.lg, backgroundColor: colors.ink }, register: { minHeight: 56, flexDirection: 'row', alignItems: 'center', justifyContent: 'space-between', paddingHorizontal: spacing.lg, backgroundColor: colors.acid }, registerText: { color: colors.ink, fontFamily: fonts.mono, fontSize: 11, fontWeight: '900' }, arrow: { color: colors.ink, fontSize: 18, fontWeight: '900' },
});
