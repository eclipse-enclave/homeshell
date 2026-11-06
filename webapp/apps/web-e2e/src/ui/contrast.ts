import { type Locator, type Page } from '@playwright/test';

/**
 * The contrast ratio (WCAG 2) of an element's text against what it is painted
 * on: its own background composited over its ancestors'. A helper module, not
 * a spec (see workstream-helpers.ts).
 */
export async function textContrast(element: Locator): Promise<number> {
  const layers = await element.evaluate((node) => {
    const view = globalThis as unknown as {
      getComputedStyle: (e: unknown) => {
        color: string;
        backgroundColor: string;
      };
    };
    type Node = { parentElement: Node | null };
    const backgrounds: string[] = [];
    for (let at: Node | null = node as Node; at; at = at.parentElement) {
      backgrounds.push(view.getComputedStyle(at).backgroundColor);
    }
    return { color: view.getComputedStyle(node).color, backgrounds };
  });
  const parse = (css: string) => {
    const [r, g, b, a = 1] = (css.match(/[\d.]+/g) ?? []).map(Number);
    return { r, g, b, a };
  };
  // Painted bottom-up, from white under the root.
  let ground = { r: 255, g: 255, b: 255 };
  for (const layer of layers.backgrounds.reverse().map(parse)) {
    ground = {
      r: layer.r * layer.a + ground.r * (1 - layer.a),
      g: layer.g * layer.a + ground.g * (1 - layer.a),
      b: layer.b * layer.a + ground.b * (1 - layer.a),
    };
  }
  const luminance = ({ r, g, b }: { r: number; g: number; b: number }) => {
    const [lr, lg, lb] = [r, g, b].map((channel) => {
      const c = channel / 255;
      return c <= 0.03928 ? c / 12.92 : ((c + 0.055) / 1.055) ** 2.4;
    });
    return 0.2126 * lr + 0.7152 * lg + 0.0722 * lb;
  };
  const [lighter, darker] = [
    luminance(parse(layers.color)),
    luminance(ground),
  ].sort((a, b) => b - a);
  return (lighter + 0.05) / (darker + 0.05);
}

/** The computed value of one of an element's CSS properties. */
export function styleOf(element: Locator, property: string): Promise<string> {
  return element.evaluate(
    (node, name) =>
      (
        globalThis as unknown as {
          getComputedStyle: (e: unknown) => {
            getPropertyValue: (property: string) => string;
          };
        }
      )
        .getComputedStyle(node)
        .getPropertyValue(name),
    property,
  );
}

/** Loads the app in `theme` from then on, whatever the system prefers. */
export async function useTheme(page: Page, theme: 'light' | 'dark') {
  await page.addInitScript(
    (chosen) =>
      (
        globalThis as unknown as {
          localStorage: { setItem: (key: string, value: string) => void };
        }
      ).localStorage.setItem('hs-theme', chosen),
    theme,
  );
}
