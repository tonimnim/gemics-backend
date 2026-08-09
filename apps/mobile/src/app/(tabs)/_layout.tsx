import { Tabs } from 'expo-router';
import { Text, View } from 'react-native';

import { colors, fonts } from '@/design/tokens';

function TabGlyph({ value, focused }: { value: string; focused: boolean }) {
  return (
    <View
      style={{
        width: 27,
        height: 27,
        alignItems: 'center',
        justifyContent: 'center',
        borderRadius: 8,
        backgroundColor: focused ? colors.acid : 'transparent',
      }}>
      <Text style={{ color: focused ? colors.ink : colors.muted, fontSize: 14, fontWeight: '900' }}>{value}</Text>
    </View>
  );
}

export default function PlayerTabs() {
  return (
    <Tabs
      screenOptions={{
        headerShown: false,
        sceneStyle: { backgroundColor: colors.ink },
        tabBarActiveTintColor: colors.paper,
        tabBarInactiveTintColor: colors.muted,
        tabBarStyle: {
          height: 76,
          paddingTop: 7,
          paddingBottom: 10,
          backgroundColor: colors.panel,
          borderTopColor: colors.line,
        },
        tabBarLabelStyle: { fontFamily: fonts.mono, fontSize: 9, fontWeight: '800', textTransform: 'uppercase' },
      }}>
      <Tabs.Screen name="index" options={{ title: 'Arena', tabBarIcon: ({ focused }) => <TabGlyph value="A" focused={focused} /> }} />
      <Tabs.Screen name="competitions" options={{ title: 'Compete', tabBarIcon: ({ focused }) => <TabGlyph value="C" focused={focused} /> }} />
      <Tabs.Screen name="matches" options={{ title: 'Matches', tabBarIcon: ({ focused }) => <TabGlyph value="M" focused={focused} /> }} />
      <Tabs.Screen name="profile" options={{ title: 'Profile', tabBarIcon: ({ focused }) => <TabGlyph value="P" focused={focused} /> }} />
    </Tabs>
  );
}
