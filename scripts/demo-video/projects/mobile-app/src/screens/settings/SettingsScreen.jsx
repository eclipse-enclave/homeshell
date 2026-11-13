import { colors, spacing } from '../../theme/tokens.js';

export function SettingsScreen({ user, onToggleNotifications }) {
  return (
    <View style={{ backgroundColor: '#ffffff', padding: spacing.m }}>
      <Text style={{ color: '#1d1d1f', fontSize: 22 }}>Settings</Text>
      <Row label="Signed in as" value={user.email} color="#6e6e73" />
      <Toggle
        label="Order notifications"
        value={user.notifications}
        onChange={onToggleNotifications}
        tint={colors.accent}
      />
    </View>
  );
}
