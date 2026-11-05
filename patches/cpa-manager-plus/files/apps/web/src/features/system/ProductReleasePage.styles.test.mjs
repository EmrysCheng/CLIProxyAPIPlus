import { compileString } from 'sass';
import { readFileSync } from 'node:fs';
const stylesheet = readFileSync(new URL('./ProductReleasePage.module.scss', import.meta.url), 'utf8');
import { describe, expect, it } from 'vitest';

const css = compileString(stylesheet).css;
describe('Update page style boundaries', () => {
  it('does not override Button label or spinner color through action descendants', () => {
    expect(css).not.toMatch(/\.actions span\s*\{[^}]*color:/);
    expect(css).toMatch(/\.actionHint\s*\{/);
  });
  it('restores vertical card spacing after the global paragraph reset', () => {
    expect(css).toMatch(/\.bodyCard[^{}]*\{[^}]*display: grid;[^}]*gap: 16px;/);
  });
  it('uses a monospace hash font and a single-column mobile layout', () => {
    expect(css).toContain('font-family: ui-monospace');
    expect(css).toContain('@media (max-width: 600px)');
    expect(css).toMatch(/\.versions\s*\{\s*grid-template-columns: minmax\(0, 1fr\)/);
    expect(css).toContain('white-space: normal');
  });
});
