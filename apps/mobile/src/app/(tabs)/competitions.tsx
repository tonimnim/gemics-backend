import { Feather } from '@expo/vector-icons';
import { useMemo, useState } from 'react';
import { ScrollView, StyleSheet, Text, TextInput, View } from 'react-native';
import { SafeAreaView } from 'react-native-safe-area-context';

import { CompetitionCard } from '@/components/product';
import { featuredCompetitions } from '@/features/competitions/demo';
import { colors, fonts, radius, spacing } from '@/design/tokens';

export default function CompetitionsScreen() {
  const [query, setQuery] = useState('');
  const visibleCompetitions = useMemo(() => {
    const normalized = query.trim().toLocaleLowerCase();
    if (!normalized) return featuredCompetitions;
    return featuredCompetitions.filter((competition) =>
      [competition.name, competition.description, competition.mode].some((value) =>
        value.toLocaleLowerCase().includes(normalized),
      ),
    );
  }, [query]);

  return (
    <SafeAreaView style={styles.safeArea} edges={['top']}>
      <ScrollView contentContainerStyle={styles.content} showsVerticalScrollIndicator={false}>
        <View style={styles.header}>
          <View>
            <Text style={styles.title}>Competitions</Text>
            <Text style={styles.body}>Find your next eFootball Mobile event.</Text>
          </View>
          <View style={styles.demoBadge}><View style={styles.demoDot} /><Text style={styles.demoText}>DEMO</Text></View>
        </View>

        <View style={styles.searchBox}>
          <Feather color={colors.muted} name="search" size={18} />
          <TextInput
            accessibilityLabel="Search competitions"
            autoCorrect={false}
            onChangeText={setQuery}
            placeholder="Search competitions"
            placeholderTextColor={colors.muted}
            returnKeyType="search"
            style={styles.searchInput}
            value={query}
          />
        </View>

        <View style={styles.filterRow}>
          <View style={styles.activeFilter}><Text style={styles.activeFilterText}>Open</Text></View>
          <Text style={styles.filter}>Upcoming</Text>
          <Text style={styles.filter}>My events</Text>
        </View>

        <View style={styles.sectionHeading}>
          <Text style={styles.sectionTitle}>Available now</Text>
          <Text style={styles.count}>{visibleCompetitions.length}</Text>
        </View>
        <View style={styles.list}>
          {visibleCompetitions.map((competition) => <CompetitionCard competition={competition} key={competition.id} />)}
          {visibleCompetitions.length === 0 ? (
            <View style={styles.empty}><Text style={styles.emptyTitle}>No competitions found</Text><Text style={styles.emptyBody}>Try a shorter name or game mode.</Text></View>
          ) : null}
        </View>
      </ScrollView>
    </SafeAreaView>
  );
}

const styles = StyleSheet.create({
  safeArea: { flex: 1, backgroundColor: colors.ink },
  content: { paddingHorizontal: spacing.lg, paddingTop: spacing.sm, paddingBottom: spacing.xxl },
  header: { minHeight: 58, flexDirection: 'row', alignItems: 'flex-start', justifyContent: 'space-between' },
  title: { color: colors.paper, fontSize: 30, lineHeight: 36, letterSpacing: -1, fontWeight: '900' },
  body: { color: colors.muted, marginTop: 2, fontSize: 12 },
  demoBadge: { marginTop: 4, paddingHorizontal: 9, paddingVertical: 6, flexDirection: 'row', alignItems: 'center', gap: 6, borderRadius: 999, backgroundColor: colors.panel },
  demoDot: { width: 6, height: 6, borderRadius: 3, backgroundColor: colors.orange },
  demoText: { color: colors.muted, fontFamily: fonts.mono, fontSize: 7, fontWeight: '900' },
  searchBox: { height: 48, marginTop: spacing.md, paddingHorizontal: spacing.md, flexDirection: 'row', alignItems: 'center', gap: spacing.sm, borderRadius: radius.md, borderWidth: 1, borderColor: colors.line, backgroundColor: colors.panel },
  searchInput: { flex: 1, height: '100%', paddingVertical: 0, color: colors.paper, fontSize: 14 },
  filterRow: { minHeight: 44, marginTop: spacing.md, flexDirection: 'row', alignItems: 'center', gap: spacing.lg },
  activeFilter: { minHeight: 32, paddingHorizontal: 13, alignItems: 'center', justifyContent: 'center', borderRadius: 16, backgroundColor: colors.acid },
  activeFilterText: { color: colors.ink, fontSize: 11, fontWeight: '900' },
  filter: { color: colors.muted, fontSize: 11, fontWeight: '800' },
  sectionHeading: { marginTop: spacing.lg, marginBottom: spacing.sm, flexDirection: 'row', alignItems: 'center', justifyContent: 'space-between' },
  sectionTitle: { color: colors.paper, fontSize: 17, fontWeight: '800' },
  count: { color: colors.muted, fontFamily: fonts.mono, fontSize: 9 },
  list: { gap: spacing.sm },
  empty: { minHeight: 180, alignItems: 'center', justifyContent: 'center', borderTopWidth: 1, borderBottomWidth: 1, borderColor: colors.line },
  emptyTitle: { color: colors.paper, fontSize: 16, fontWeight: '800' },
  emptyBody: { color: colors.muted, marginTop: 5, fontSize: 12 },
});
