import { Image, StyleSheet, Text, View } from 'react-native';

import { colors, fonts, radius } from '@/design/tokens';

type PlayerAvatarProps = {
  name: string;
  uri: string;
  size?: number;
  accessible?: boolean;
};

export function PlayerAvatar({ name, uri, size = 46, accessible = false }: PlayerAvatarProps) {
  const initials = name
    .split(' ')
    .slice(0, 2)
    .map((part) => part[0])
    .join('');

  return (
    <View style={[styles.avatar, { width: size, height: size, borderRadius: size / 2 }]}>
      <Text style={[styles.initials, { fontSize: size * 0.3 }]}>{initials}</Text>
      <Image
        accessibilityLabel={accessible ? `${name}'s profile photo` : undefined}
        accessible={accessible}
        source={{ uri }}
        resizeMode="cover"
        style={[StyleSheet.absoluteFill, { borderRadius: size / 2 }]}
      />
    </View>
  );
}

export function Movement({ value }: { value: number }) {
  const improved = value > 0;
  const dropped = value < 0;
  const label = improved ? `Up ${value}` : dropped ? `Down ${Math.abs(value)}` : 'No change';

  return (
    <View accessibilityLabel={label} style={styles.movement}>
      <Text style={[styles.movementText, improved && styles.up, dropped && styles.down]}>
        {improved ? '↑' : dropped ? '↓' : '–'} {value === 0 ? '' : Math.abs(value)}
      </Text>
    </View>
  );
}

export function RankingSkeleton() {
  return (
    <View accessibilityLabel="Loading player rankings" accessibilityRole="progressbar">
      {Array.from({ length: 7 }, (_, index) => (
        <View key={index} style={styles.skeletonRow}>
          <View style={[styles.skeleton, styles.skeletonRank]} />
          <View style={[styles.skeleton, styles.skeletonAvatar]} />
          <View style={styles.skeletonCopy}>
            <View style={[styles.skeleton, styles.skeletonName]} />
            <View style={[styles.skeleton, styles.skeletonHandle]} />
          </View>
          <View style={[styles.skeleton, styles.skeletonRating]} />
        </View>
      ))}
    </View>
  );
}

const styles = StyleSheet.create({
  avatar: {
    alignItems: 'center',
    justifyContent: 'center',
    overflow: 'hidden',
    backgroundColor: colors.blue,
    borderWidth: 1,
    borderColor: colors.line,
  },
  initials: { color: colors.paper, fontWeight: '900' },
  movement: { minWidth: 38, alignItems: 'flex-end' },
  movementText: { color: colors.muted, fontFamily: fonts.mono, fontSize: 10, fontWeight: '800' },
  up: { color: colors.green },
  down: { color: colors.orange },
  skeletonRow: {
    height: 76,
    flexDirection: 'row',
    alignItems: 'center',
    gap: 12,
    borderBottomWidth: StyleSheet.hairlineWidth,
    borderBottomColor: colors.line,
  },
  skeleton: { backgroundColor: colors.panel, borderRadius: radius.sm },
  skeletonRank: { width: 22, height: 14 },
  skeletonAvatar: { width: 44, height: 44, borderRadius: 22 },
  skeletonCopy: { flex: 1, gap: 7 },
  skeletonName: { width: '64%', height: 12 },
  skeletonHandle: { width: '42%', height: 8 },
  skeletonRating: { width: 42, height: 16 },
});
