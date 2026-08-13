import { Feather } from '@expo/vector-icons';
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
      <Pressable accessibilityLabel={`Open ${competition.name}`} style={({ pressed }) => [styles.card, pressed && styles.cardPressed]}>
        <View style={[styles.cardAccent, { backgroundColor: competition.accent }]} />
        <View style={styles.cardContent}>
          <View style={styles.cardTopline}><Text style={styles.cardGame}>eFOOTBALL MOBILE</Text><Text style={styles.cardStatus}>{competition.status}</Text></View>
          <Text numberOfLines={1} style={styles.cardTitle}>{competition.name}</Text>
          <Text style={styles.cardMeta}>{competition.mode} · {competition.players} players</Text>
          <View style={styles.capacityRow}><View style={styles.capacityTrack}><View style={[styles.capacityValue, { width: competition.capacity, backgroundColor: competition.accent }]} /></View></View>
        </View>
        <Feather color={colors.muted} name="chevron-right" size={20} />
      </Pressable>
    </Link>
  );
}

const styles = StyleSheet.create({
  brand: { flexDirection: 'row', alignItems: 'center', gap: 8 }, mark: { width: 31, height: 31, alignItems: 'center', justifyContent: 'center', backgroundColor: colors.acid, borderWidth: 2, borderColor: colors.ink, transform: [{ skewX: '-7deg' }] }, markText: { color: colors.ink, fontSize: 16, fontWeight: '900' }, brandText: { color: colors.paper, fontSize: 12, fontWeight: '900', letterSpacing: 2 },
  eyebrow: { flexDirection: 'row', alignItems: 'center', gap: 8, marginBottom: spacing.sm }, eyebrowLine: { width: 22, height: 3, backgroundColor: colors.acid }, eyebrowText: { color: colors.muted, fontFamily: fonts.mono, fontSize: 8, fontWeight: '800', letterSpacing: 1 },
  stat: { flex: 1 }, statValue: { color: colors.paper, fontSize: 17, fontWeight: '900' }, statLabel: { color: colors.muted, marginTop: 2, fontFamily: fonts.mono, fontSize: 7 }, darkText: { color: colors.ink }, darkMuted: { color: colors.subtleInk },
  card: { minHeight: 108, paddingRight: 12, flexDirection: 'row', alignItems: 'center', borderRadius: radius.md, borderWidth: 1, borderColor: colors.line, backgroundColor: colors.panel },
  cardPressed: { opacity: 0.7 },
  cardAccent: { width: 4, alignSelf: 'stretch', borderTopLeftRadius: radius.md, borderBottomLeftRadius: radius.md },
  cardContent: { flex: 1, minWidth: 0, paddingHorizontal: 13, paddingVertical: 12 },
  cardTopline: { flexDirection: 'row', alignItems: 'center', justifyContent: 'space-between', gap: spacing.sm },
  cardGame: { color: colors.muted, fontFamily: fonts.mono, fontSize: 7, fontWeight: '800' },
  cardStatus: { flexShrink: 1, color: colors.acid, fontSize: 9, fontWeight: '800' },
  cardTitle: { color: colors.paper, marginTop: 7, fontSize: 15, fontWeight: '900' },
  cardMeta: { color: colors.muted, marginTop: 4, fontSize: 10 },
  capacityRow: { marginTop: 10 },
  capacityTrack: { height: 3, overflow: 'hidden', borderRadius: 2, backgroundColor: colors.line },
  capacityValue: { height: 3, borderRadius: 2 },
});
