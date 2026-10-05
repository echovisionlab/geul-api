import assert from 'node:assert/strict';
import test from 'node:test';
import { ESLint, Linter } from 'eslint';
import { noNestedIf } from '../eslint/no-nested-if.mjs';

const config = {
  languageOptions: { ecmaVersion: 'latest', sourceType: 'module' },
  plugins: { housekeeping: { rules: { 'no-nested-if': noNestedIf } } },
  rules: { 'housekeeping/no-nested-if': 'error' },
};

function lint(source) {
  return new Linter({ configType: 'flat' }).verify(source, config);
}

test('rejects an if nested in another if', () => {
  const messages = lint('if (outer) { if (inner) work(); }');
  assert.equal(messages.length, 1);
  assert.equal(messages[0].ruleId, 'housekeeping/no-nested-if');
});

test('allows guard clauses and else-if chains', () => {
  assert.deepEqual(lint('function work() { if (!ready) return; if (done) return; }'), []);
  assert.deepEqual(lint('if (first) work(); else if (second) retry(); else finish();'), []);
});

test('checks test files through the repository ESLint config', async () => {
  const eslint = new ESLint({ overrideConfigFile: 'eslint.config.mjs' });
  const [result] = await eslint.lintText('if (outer) { if (inner) work(); }', {
    filePath: 'src/logger.test.ts',
  });
  assert.equal(
    result.messages.some((message) => message.ruleId === 'housekeeping/no-nested-if'),
    true
  );
});
