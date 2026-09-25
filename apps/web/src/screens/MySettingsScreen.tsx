/**
 * Your settings — this browser's, not the organization's: how dude looks.
 * Kept by the design system's ThemeProvider in local storage. (Reduced
 * motion follows the system setting.)
 */

import { useTheme } from "@dude/design-system";
import { Card, CardBody, CardHeader, Select } from "@dude/design-system/primitives";

export function MySettingsScreen() {
  const theme = useTheme();
  return (
    <div className="settingsScreen" data-testid="my-settings">
      <header className="settingsHeader">
        <h1 className="wiTitle">You</h1>
        <span className="muted">This browser</span>
      </header>
      <Card>
        <CardHeader title="Appearance" />
        <CardBody>
          <div className="settingsForm">
            <Select
              label="Theme"
              value={theme.preference}
              onValueChange={theme.setPreference}
              options={[
                { value: "system", label: "Follow the system" },
                { value: "dark", label: "Dark" },
                { value: "light", label: "Light" },
              ]}
            />
            <Select
              label="Density"
              value={theme.density}
              onValueChange={theme.setDensity}
              options={[
                { value: "comfortable", label: "Comfortable" },
                { value: "compact", label: "Compact — more on screen" },
              ]}
            />
          </div>
        </CardBody>
      </Card>
    </div>
  );
}
