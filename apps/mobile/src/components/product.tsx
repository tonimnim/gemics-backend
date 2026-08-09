import { Link } from 'expo-router';
import { Pressable, StyleSheet, Text, View } from 'react-native';

import { CompetitionSummary } from '@/features/competitions/types';
import { colors, fonts, radius, spacing } from '@/design/tokens';

export function BrandMark() {
  return <View style={styles.brand}><View style={styles.mark}><Text style={styles.markText}>G</Text></View><Text style={styles.brandText}>GAMICS</Text></View>;
}

export function Eyebrow({ label }: { label: string }) {
  return <View style={styles.eyebrow}><View style={styles.eyebrowLine} /><Text style={styles.eyebrowText}>{label}</Text></View>;
}

export function Stat({ value, label, dark = false }: { value: string; label: string; dark?: boolean }) {
  return <View style={styles.stat}><Text style={[styles.statValue, dark && styles.darkText]}>{value}</Text><Text style={[styles.statLabel, dark && styles.darkMuted]}>{label}</Text></View>;
}

export function CompetitionCard({ competition }: { competition: CompetitionSummary }) {
  return (
    <Link href={{ pathname: '/competition/[id]', params: { id: competition.id } }} asChild>
      <Pressable style={styles.card}>
        <View style={[styles.cardNumber, { backgroundColor: competition.accent }]}><Text style={styles.cardNumberText}>{competition.number}</Text><Text style={styles.cardArrow}>↗</Text></View>
        <View style={styles.cardContent}><Text style={styles.cardGame}>eFOOTBALL MOBILE</Text><Text style={styles.cardTitle}>{competition.name}</Text><Text style={styles.cardMeta}>{competition.mode}  •  {competition.status}</Text><View style={styles.capacityRow}><View style={styles.capacityTrack}><View style={[styles.capacityValue, { width: competition.capacity }]} /></View><Text style={styles.capacityText}>{competition.players}</Text></View></View>
      </Pressable>
    </Link>
  );
}

const styles = StyleSheet.create({
  brand: { flexDirection: 'row', alignItems: 'center', gap: 8 }, mark: { width: 31, height: 31, alignItems: 'center', justifyContent: 'center', backgroundColor: colors.acid, borderWidth: 2, borderColor: colors.ink, transform: [{ skewX: '-7deg' }] }, markText: { color: colors.ink, fontSize: 16, fontWeight: '900' }, brandText: { color: colors.paper, fontSize: 12, fontWeight: '900', letterSpacing: 2 },
  eyebrow: { flexDirection: 'row', alignItems: 'center', gap: 8, marginBottom: spacing.sm }, eyebrowLine: { width: 22, height: 3, backgroundColor: colors.acid }, eyebrowText: { color: colors.muted, fontFamily: fonts.mono, fontSize: 8, fontWeight: '800', letterSpacing: 1 },
  stat: { flex: 1 }, statValue: { color: colors.paper, fontSize: 17, fontWeight: '900' }, statLabel: { color: colors.muted, marginTop: 2, fontFamily: fonts.mono, fontSize: 7 }, darkText: { color: colors.ink }, darkMuted: { color: colors.subtleInk },
  card: { overflow: 'hidden', flexDirection: 'row', minHeight: 158, backgroundColor: colors.paper, borderRadius: radius.md }, cardNumber: { width: 92, paddingVertical: spacing.md, alignItems: 'center', justifyContent: 'space-between' }, cardNumberText: { color: colors.ink, fontSize: 47, lineHeight: 50, fontWeight: '900', letterSpacing: -4 }, cardArrow: { color: colors.ink, fontSize: 17, fontWeight: '900' }, cardContent: { flex: 1, padding: spacing.md }, cardGame: { color: colors.subtleInk, fontFamily: fonts.mono, fontSize: 7, fontWeight: '800' }, cardTitle: { color: colors.ink, marginTop: 7, fontSize: 17, fontWeight: '900' }, cardMeta: { color: colors.subtleInk, marginTop: 4, fontSize: 10 }, capacityRow: { flexDirection: 'row', alignItems: 'center', gap: spacing.sm, marginTop: 'auto' }, capacityTrack: { flex: 1, height: 4, backgroundColor: colors.softLine }, capacityValue: { height: 4, backgroundColor: colors.ink }, capacityText: { color: colors.ink, fontFamily: fonts.mono, fontSize: 8, fontWeight: '800' },
});
