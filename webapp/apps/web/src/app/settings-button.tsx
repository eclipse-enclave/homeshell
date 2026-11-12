import { SettingsIcon } from '@homeshell/components';
import { TopBarButton } from './top-bar-button';

export interface SettingsButtonProps {
  onClick: () => void;
}

/** Top-bar icon button that opens the settings page. */
export function SettingsButton({ onClick }: SettingsButtonProps) {
  return (
    <TopBarButton
      label="Settings"
      icon={<SettingsIcon size={16} />}
      onClick={onClick}
    />
  );
}
