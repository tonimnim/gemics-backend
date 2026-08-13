import { router } from 'expo-router';
import { useEffect, useMemo, useState } from 'react';
import {
  FlatList,
  ListRenderItemInfo,
  Pressable,
  StyleSheet,
  Text,
  TextInput,
  View,
} from 'react-native';
import { SafeAreaView } from 'react-native-safe-area-context';

import { colors, fonts, radius, spacing } from '@/design/tokens';
import { Movement, PlayerAvatar, RankingSkeleton } from '@/features/players/components';
import { rankedPlayers } from '@/features/players/demo';
import { RankedPlayer, RankingScope, RankingSort } from '@/features/players/types';

const ROW_HEIGHT = 76;
const sortOptions: { label: string; value: RankingSort }[] = [
  { label: 'Rank', value: 'rank' },
  { label: 'Rating', value: 'rating' },
  { label: 'Wins', value: 'wins' },
];

export default function RankingsScreen() {
  const [query, setQuery] = useState('');
  const [scope, setScope] = useState<RankingScope>('national');
  const [sort, setSort] = useState<RankingSort>('rank');
  const [isLoading, setIsLoading] = useState(true);

  useEffect(() => {
    const loadingTimer = setTimeout(() => setIsLoading(false), 280);
    return () => clearTimeout(loadingTimer);
  }, []);

  const visiblePlayers = useMemo(() => {
    const normalizedQuery = query.trim().toLocaleLowerCase();
    const matches = normalizedQuery
      ? rankedPlayers.filter((player) =>
          [player.name, player.handle, player.county].some((value) =>
            value.toLocaleLowerCase().includes(normalizedQuery),
          ),
        )
      : rankedPlayers;

    return [...matches].sort((left, right) => {
      if (sort === 'rating') return right.rating - left.rating;
      if (sort === 'wins') return right.wins - left.wins;
      return scope === 'national'
        ? left.nationalRank - right.nationalRank
        : left.globalRank - right.globalRank;
    });
  }, [query, scope, sort]);

  const renderPlayer = ({ item }: ListRenderItemInfo<RankedPlayer>) => {
    const rank = scope === 'national' ? item.nationalRank : item.globalRank;

    return (
      <Pressable
        accessibilityHint="Opens this player's competitive profile"
        accessibilityLabel={`Rank ${rank}, ${item.name}, ${item.rating} rating`}
        accessibilityRole="button"
        android_ripple={{ color: colors.line }}
        onPress={() => router.push({ pathname: '/player/[id]', params: { id: item.id } })}
        style={({ pressed }) => [styles.playerRow, pressed && styles.playerRowPressed]}>
        <Text style={styles.rank}>{rank}</Text>
        <PlayerAvatar name={item.name} uri={item.avatarUrl} size={44} />
        <View style={styles.identity}>
          <Text numberOfLines={1} style={styles.playerName}>{item.name}</Text>
          <Text numberOfLines={1} style={styles.handle}>{item.handle} · {item.county}</Text>
        </View>
        <View style={styles.ratingColumn}>
          <Text style={styles.rating}>{item.rating.toLocaleString()}</Text>
          <Text style={styles.ratingLabel}>RATING</Text>
        </View>
        <Movement value={item.rankMovement} />
      </Pressable>
    );
  };

  return (
    <SafeAreaView edges={['top']} style={styles.safeArea}>
      <View style={styles.header}>
        <View style={styles.titleRow}>
          <View>
            <Text style={styles.title}>Rankings</Text>
            <Text style={styles.subtitle}>{rankedPlayers.length.toLocaleString()} ranked players</Text>
          </View>
          <View style={styles.seasonBadge}>
            <View style={styles.liveDot} />
            <Text style={styles.seasonText}>LIVE</Text>
          </View>
        </View>

        <View style={styles.searchBox}>
          <Text accessibilityElementsHidden style={styles.searchIcon}>⌕</Text>
          <TextInput
            accessibilityLabel="Search ranked players"
            autoCapitalize="none"
            autoCorrect={false}
            clearButtonMode="while-editing"
            onChangeText={setQuery}
            placeholder="Search name, handle or county"
            placeholderTextColor={colors.muted}
            returnKeyType="search"
            style={styles.searchInput}
            value={query}
          />
          {query.length > 0 ? (
            <Pressable
              accessibilityLabel="Clear player search"
              accessibilityRole="button"
              hitSlop={10}
              onPress={() => setQuery('')}>
              <Text style={styles.clear}>×</Text>
            </Pressable>
          ) : null}
        </View>

        <View style={styles.controls}>
          <View accessibilityLabel="Ranking scope" style={styles.scopeControl}>
            {(['national', 'global'] as RankingScope[]).map((value) => (
              <Pressable
                accessibilityRole="button"
                accessibilityState={{ selected: scope === value }}
                key={value}
                onPress={() => setScope(value)}
                style={[styles.scopeButton, scope === value && styles.scopeButtonActive]}>
                <Text style={[styles.scopeText, scope === value && styles.scopeTextActive]}>
                  {value === 'national' ? 'Kenya' : 'Global'}
                </Text>
              </Pressable>
            ))}
          </View>

          <View style={styles.sortRow}>
            {sortOptions.map((option) => (
              <Pressable
                accessibilityRole="button"
                accessibilityState={{ selected: sort === option.value }}
                key={option.value}
                onPress={() => setSort(option.value)}
                style={[styles.sortButton, sort === option.value && styles.sortButtonActive]}>
                <Text style={[styles.sortText, sort === option.value && styles.sortTextActive]}>
                  {option.label}
                </Text>
              </Pressable>
            ))}
          </View>
        </View>

        <View style={styles.columnLabels}>
          <Text style={[styles.columnLabel, styles.rankLabel]}>#</Text>
          <Text style={[styles.columnLabel, styles.playerLabel]}>PLAYER</Text>
          <Text style={styles.columnLabel}>RATING</Text>
          <Text style={[styles.columnLabel, styles.changeLabel]}>MOVE</Text>
        </View>
      </View>

      {isLoading ? (
        <View style={styles.loadingWrap}><RankingSkeleton /></View>
      ) : (
        <FlatList
          contentContainerStyle={[styles.listContent, visiblePlayers.length === 0 && styles.emptyList]}
          data={visiblePlayers}
          getItemLayout={(_, index) => ({ index, length: ROW_HEIGHT, offset: ROW_HEIGHT * index })}
          initialNumToRender={12}
          keyboardDismissMode="on-drag"
          keyboardShouldPersistTaps="handled"
          keyExtractor={(player) => player.id}
          ListEmptyComponent={
            <View accessibilityLiveRegion="polite" style={styles.emptyState}>
              <Text style={styles.emptyIcon}>⌕</Text>
              <Text style={styles.emptyTitle}>No players found</Text>
              <Text style={styles.emptyBody}>Try another name, handle or county.</Text>
              <Pressable accessibilityRole="button" onPress={() => setQuery('')} style={styles.resetButton}>
                <Text style={styles.resetText}>Clear search</Text>
              </Pressable>
            </View>
          }
          maxToRenderPerBatch={12}
          removeClippedSubviews
          renderItem={renderPlayer}
          showsVerticalScrollIndicator={false}
          updateCellsBatchingPeriod={40}
          windowSize={9}
        />
      )}
    </SafeAreaView>
  );
}

const styles = StyleSheet.create({
  safeArea: { flex: 1, backgroundColor: colors.ink },
  header: { paddingHorizontal: spacing.lg, paddingTop: spacing.sm },
  titleRow: { flexDirection: 'row', alignItems: 'center', justifyContent: 'space-between' },
  title: { color: colors.paper, fontSize: 30, lineHeight: 36, fontWeight: '900', letterSpacing: -1 },
  subtitle: { color: colors.muted, marginTop: 2, fontSize: 12 },
  seasonBadge: { flexDirection: 'row', alignItems: 'center', gap: 6 },
  liveDot: { width: 6, height: 6, borderRadius: 3, backgroundColor: colors.green },
  seasonText: { color: colors.muted, fontFamily: fonts.mono, fontSize: 8, fontWeight: '900' },
  searchBox: {
    height: 48,
    marginTop: spacing.lg,
    paddingHorizontal: spacing.md,
    flexDirection: 'row',
    alignItems: 'center',
    gap: spacing.sm,
    borderRadius: radius.md,
    backgroundColor: colors.panel,
    borderWidth: 1,
    borderColor: colors.line,
  },
  searchIcon: { color: colors.muted, fontSize: 22, lineHeight: 24 },
  searchInput: { flex: 1, height: '100%', color: colors.paper, fontSize: 14, paddingVertical: 0 },
  clear: { color: colors.paper, fontSize: 22, lineHeight: 24 },
  controls: { marginTop: spacing.md, alignItems: 'flex-start', gap: spacing.sm },
  scopeControl: { flexDirection: 'row', padding: 3, borderRadius: radius.sm, backgroundColor: colors.panel },
  scopeButton: { minHeight: 30, paddingHorizontal: 11, alignItems: 'center', justifyContent: 'center', borderRadius: 6 },
  scopeButtonActive: { backgroundColor: colors.paper },
  scopeText: { color: colors.muted, fontSize: 11, fontWeight: '700' },
  scopeTextActive: { color: colors.ink },
  sortRow: { width: '100%', flexDirection: 'row', alignItems: 'center', gap: 4 },
  sortButton: { minHeight: 32, paddingHorizontal: 8, alignItems: 'center', justifyContent: 'center' },
  sortButtonActive: { borderBottomWidth: 2, borderBottomColor: colors.acid },
  sortText: { color: colors.muted, fontSize: 10, fontWeight: '700' },
  sortTextActive: { color: colors.paper },
  columnLabels: {
    height: 36,
    marginTop: spacing.sm,
    flexDirection: 'row',
    alignItems: 'center',
    borderBottomWidth: StyleSheet.hairlineWidth,
    borderBottomColor: colors.line,
  },
  columnLabel: { color: colors.muted, fontFamily: fonts.mono, fontSize: 7, fontWeight: '800', letterSpacing: 0.6 },
  rankLabel: { width: 34 },
  playerLabel: { flex: 1, marginLeft: 56 },
  changeLabel: { width: 42, textAlign: 'right' },
  loadingWrap: { flex: 1, paddingHorizontal: spacing.lg },
  listContent: { paddingHorizontal: spacing.lg, paddingBottom: spacing.xxl },
  emptyList: { flexGrow: 1 },
  playerRow: {
    height: ROW_HEIGHT,
    flexDirection: 'row',
    alignItems: 'center',
    gap: 12,
    borderBottomWidth: StyleSheet.hairlineWidth,
    borderBottomColor: colors.line,
  },
  playerRowPressed: { opacity: 0.65 },
  rank: { width: 22, color: colors.paper, fontFamily: fonts.mono, fontSize: 12, fontWeight: '900', textAlign: 'center' },
  identity: { flex: 1, minWidth: 0 },
  playerName: { color: colors.paper, fontSize: 14, fontWeight: '800' },
  handle: { color: colors.muted, marginTop: 3, fontSize: 10 },
  ratingColumn: { alignItems: 'flex-end' },
  rating: { color: colors.paper, fontFamily: fonts.mono, fontSize: 12, fontWeight: '900' },
  ratingLabel: { color: colors.muted, marginTop: 2, fontFamily: fonts.mono, fontSize: 6 },
  emptyState: { flex: 1, alignItems: 'center', justifyContent: 'center', paddingHorizontal: spacing.xl, paddingBottom: spacing.xxl },
  emptyIcon: { color: colors.muted, fontSize: 32 },
  emptyTitle: { color: colors.paper, marginTop: spacing.md, fontSize: 18, fontWeight: '900' },
  emptyBody: { color: colors.muted, marginTop: spacing.xs, fontSize: 13, textAlign: 'center' },
  resetButton: { minHeight: 42, marginTop: spacing.lg, paddingHorizontal: spacing.lg, alignItems: 'center', justifyContent: 'center', borderRadius: radius.sm, backgroundColor: colors.acid },
  resetText: { color: colors.ink, fontSize: 12, fontWeight: '900' },
});
