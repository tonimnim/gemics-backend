import { Feather } from '@expo/vector-icons';
import { router, useLocalSearchParams } from 'expo-router';
import { Pressable, ScrollView, StyleSheet, Text, View } from 'react-native';
import { SafeAreaView, useSafeAreaInsets } from 'react-native-safe-area-context';

import { colors, fonts, radius, spacing } from '@/design/tokens';
import { featuredCompetitions } from '@/features/competitions/demo';

function Metric({ label, value }: { label: string; value: string }) {
  return (
    <View style={styles.metric}>
      <Text style={styles.metricValue}>{value}</Text>
      <Text style={styles.metricLabel}>{label}</Text>
    </View>
  );
}

function DetailRow({ icon, label, value }: { icon: keyof typeof Feather.glyphMap; label: string; value: string }) {
  return (
    <View style={styles.detailRow}>
      <View style={styles.detailIcon}><Feather color={colors.acid} name={icon} size={17} /></View>
      <View style={styles.detailCopy}><Text style={styles.detailLabel}>{label}</Text><Text style={styles.detailValue}>{value}</Text></View>
    </View>
  );
}

export default function CompetitionDetailsScreen() {
  const { id } = useLocalSearchParams<{ id: string }>();
  const insets = useSafeAreaInsets();
  const competition = featuredCompetitions.find((item) => item.id === id) ?? featuredCompetitions[0];

  return (
    <SafeAreaView edges={['top']} style={styles.safeArea}>
      <ScrollView contentContainerStyle={[styles.content, { paddingBottom: 116 + insets.bottom }]} showsVerticalScrollIndicator={false}>
        <View style={styles.topbar}>
          <Pressable accessibilityLabel="Go back" hitSlop={10} onPress={() => router.back()} style={styles.iconButton}>
            <Feather color={colors.paper} name="arrow-left" size={22} />
          </Pressable>
          <View style={styles.demoBadge}><View style={styles.demoDot} /><Text style={styles.demoText}>DEMO DATA</Text></View>
        </View>

        <View style={styles.heading}>
          <Text style={styles.game}>eFootball Mobile</Text>
          <Text style={styles.title}>{competition.name}</Text>
          <Text style={styles.description}>{competition.description}</Text>
        </View>

        <View style={styles.metrics}>
          <Metric label="PLAYERS" value={competition.players} />
          <View style={styles.metricDivider} />
          <Metric label="FORMAT" value={competition.mode} />
          <View style={styles.metricDivider} />
          <Metric label="ENTRY" value="Free" />
        </View>

        <View style={styles.section}>
          <Text style={styles.sectionTitle}>Tournament details</Text>
          <View style={styles.detailList}>
            <DetailRow icon="calendar" label="Schedule" value={competition.status} />
            <DetailRow icon="users" label="Mode" value={`${competition.mode} · eFootball Mobile`} />
            <DetailRow icon="git-branch" label="Progression" value="Round-by-round bracket" />
          </View>
        </View>

        <View style={styles.section}>
          <Text style={styles.sectionTitle}>How results work</Text>
          <Text style={styles.sectionBody}>After the match, attach the final-result screenshot and enter the score. Your opponent confirms it or opens a dispute for referee review.</Text>
          <View style={styles.verificationRow}>
            <Feather color={colors.green} name="shield" size={18} />
            <Text style={styles.verificationText}>Screenshot + opponent confirmation</Text>
          </View>
        </View>
      </ScrollView>

      <View style={[styles.bottom, { paddingBottom: Math.max(insets.bottom, spacing.md) }]}>
        <Pressable accessibilityState={{ disabled: true }} disabled style={styles.registerUnavailable}>
          <View><Text style={styles.registerText}>Registration API ready</Text><Text style={styles.registerHint}>Mobile auth connection comes next</Text></View>
          <Feather color={colors.subtleInk} name="lock" size={18} />
        </Pressable>
      </View>
    </SafeAreaView>
  );
}

const styles = StyleSheet.create({
  safeArea: { flex: 1, backgroundColor: colors.ink },
  content: { paddingHorizontal: spacing.lg },
  topbar: { height: 60, flexDirection: 'row', alignItems: 'center', justifyContent: 'space-between' },
  iconButton: { width: 42, height: 42, borderRadius: 21, alignItems: 'center', justifyContent: 'center', backgroundColor: colors.panel },
  demoBadge: { paddingHorizontal: 9, paddingVertical: 6, flexDirection: 'row', alignItems: 'center', gap: 6, borderRadius: 999, backgroundColor: colors.panel },
  demoDot: { width: 6, height: 6, borderRadius: 3, backgroundColor: colors.orange },
  demoText: { color: colors.muted, fontFamily: fonts.mono, fontSize: 7, fontWeight: '900' },
  heading: { paddingTop: spacing.md, paddingBottom: spacing.xl },
  game: { color: colors.acid, fontFamily: fonts.mono, fontSize: 9, fontWeight: '900', textTransform: 'uppercase' },
  title: { color: colors.paper, marginTop: spacing.sm, fontSize: 29, lineHeight: 34, fontWeight: '900', letterSpacing: -0.8 },
  description: { color: colors.muted, marginTop: spacing.sm, fontSize: 13, lineHeight: 20 },
  metrics: { minHeight: 82, flexDirection: 'row', alignItems: 'center', borderTopWidth: 1, borderBottomWidth: 1, borderColor: colors.line },
  metric: { flex: 1, alignItems: 'center', gap: 4 },
  metricValue: { color: colors.paper, fontSize: 15, fontWeight: '900' },
  metricLabel: { color: colors.muted, fontFamily: fonts.mono, fontSize: 7, letterSpacing: 0.6 },
  metricDivider: { width: 1, height: 34, backgroundColor: colors.line },
  section: { paddingVertical: spacing.xl, borderBottomWidth: 1, borderBottomColor: colors.line },
  sectionTitle: { color: colors.paper, fontSize: 18, fontWeight: '900' },
  sectionBody: { color: colors.muted, marginTop: spacing.sm, fontSize: 13, lineHeight: 20 },
  detailList: { marginTop: spacing.md },
  detailRow: { minHeight: 64, flexDirection: 'row', alignItems: 'center', gap: 12, borderBottomWidth: StyleSheet.hairlineWidth, borderBottomColor: colors.line },
  detailIcon: { width: 38, height: 38, borderRadius: 19, alignItems: 'center', justifyContent: 'center', backgroundColor: colors.panel },
  detailCopy: { flex: 1 },
  detailLabel: { color: colors.muted, fontSize: 10 },
  detailValue: { color: colors.paper, marginTop: 3, fontSize: 13, fontWeight: '800' },
  verificationRow: { minHeight: 50, marginTop: spacing.md, paddingHorizontal: 12, flexDirection: 'row', alignItems: 'center', gap: 10, borderRadius: radius.md, backgroundColor: colors.panel },
  verificationText: { flex: 1, color: colors.paper, fontSize: 12, fontWeight: '700' },
  bottom: { position: 'absolute', left: 0, right: 0, bottom: 0, paddingHorizontal: spacing.lg, paddingTop: 12, backgroundColor: colors.ink, borderTopWidth: 1, borderTopColor: colors.line },
  registerUnavailable: { minHeight: 58, paddingHorizontal: spacing.md, flexDirection: 'row', alignItems: 'center', justifyContent: 'space-between', borderRadius: radius.md, backgroundColor: colors.panel, opacity: 0.75 },
  registerText: { color: colors.paper, fontSize: 13, fontWeight: '900' },
  registerHint: { color: colors.muted, marginTop: 3, fontSize: 9 },
});
