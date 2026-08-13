import { Ionicons } from '@expo/vector-icons';
import { Tabs } from 'expo-router';
import { StyleSheet, View } from 'react-native';
import { useSafeAreaInsets } from 'react-native-safe-area-context';

import { colors } from '@/design/tokens';

type TabIconProps = {
  focused: boolean;
  name: keyof typeof Ionicons.glyphMap;
  nameFocused: keyof typeof Ionicons.glyphMap;
};

function TabIcon({ focused, name, nameFocused }: TabIconProps) {
  return (
    <View style={[styles.iconFrame, focused && styles.iconFrameFocused]}>
      <Ionicons
        color={focused ? colors.ink : colors.muted}
        name={focused ? nameFocused : name}
        size={27}
      />
    </View>
  );
}

export default function PlayerTabs() {
  const insets = useSafeAreaInsets();

  return (
    <Tabs
      screenOptions={{
        headerShown: false,
        sceneStyle: { backgroundColor: colors.ink },
        tabBarShowLabel: false,
        tabBarActiveTintColor: colors.acid,
        tabBarInactiveTintColor: colors.muted,
        tabBarHideOnKeyboard: true,
        tabBarItemStyle: styles.tabItem,
        tabBarStyle: [
          styles.tabBar,
          {
            height: 62 + insets.bottom,
            paddingBottom: Math.max(insets.bottom, 8),
          },
        ],
      }}>
      <Tabs.Screen
        name="index"
        options={{
          title: 'Home',
          tabBarAccessibilityLabel: 'Home',
          tabBarIcon: ({ focused }) => (
            <TabIcon focused={focused} name="home-outline" nameFocused="home" />
          ),
        }}
      />
      <Tabs.Screen
        name="competitions"
        options={{
          title: 'Competitions',
          tabBarAccessibilityLabel: 'Competitions',
          tabBarIcon: ({ focused }) => (
            <TabIcon focused={focused} name="trophy-outline" nameFocused="trophy" />
          ),
        }}
      />
      <Tabs.Screen
        name="matches"
        options={{
          title: 'Matches',
          tabBarAccessibilityLabel: 'Matches',
          tabBarIcon: ({ focused }) => (
            <TabIcon focused={focused} name="game-controller-outline" nameFocused="game-controller" />
          ),
        }}
      />
      <Tabs.Screen
        name="rankings"
        options={{
          title: 'Rankings',
          tabBarAccessibilityLabel: 'Player rankings',
          tabBarIcon: ({ focused }) => (
            <TabIcon focused={focused} name="podium-outline" nameFocused="podium" />
          ),
        }}
      />
      <Tabs.Screen
        name="profile"
        options={{
          title: 'Profile',
          tabBarAccessibilityLabel: 'My profile',
          tabBarIcon: ({ focused }) => (
            <TabIcon focused={focused} name="person-circle-outline" nameFocused="person-circle" />
          ),
        }}
      />
    </Tabs>
  );
}

const styles = StyleSheet.create({
  tabBar: {
    paddingTop: 7,
    backgroundColor: colors.panel,
    borderTopWidth: 1,
    borderTopColor: colors.line,
    elevation: 0,
  },
  tabItem: {
    minHeight: 52,
  },
  iconFrame: {
    width: 44,
    height: 40,
    borderRadius: 13,
    alignItems: 'center',
    justifyContent: 'center',
  },
  iconFrameFocused: {
    backgroundColor: colors.acid,
  },
});
