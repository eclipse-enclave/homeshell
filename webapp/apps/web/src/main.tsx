import { StrictMode } from 'react';
import * as ReactDOM from 'react-dom/client';
import App from './app/app';
import { CommandsProvider } from './app/keyboard/commands';
import { SettingsProvider } from './app/settings/use-settings';
import { ThemeProvider } from './app/use-theme';

const root = ReactDOM.createRoot(
  document.getElementById('root') as HTMLElement
);

root.render(
  <StrictMode>
    <ThemeProvider>
      <SettingsProvider>
        <CommandsProvider>
          <App />
        </CommandsProvider>
      </SettingsProvider>
    </ThemeProvider>
  </StrictMode>
);
