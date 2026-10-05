import eslint from '@eslint/js';
import tseslint from 'typescript-eslint';
import { noNestedIf } from './scripts/eslint/no-nested-if.mjs';

const housekeepingPlugin = {
  housekeeping: {
    rules: {
      'no-nested-if': noNestedIf,
    },
  },
};

export default tseslint.config(
  eslint.configs.recommended,
  ...tseslint.configs.recommended,
  {
    ignores: [
      'coverage/**',
      'dist/**',
      'node_modules/**',
      '**/*.js',
      '**/*.mjs',
      '**/*.cjs',
    ],
  },
  {
    files: ['src/**/*.ts', 'src/**/*.tsx'],
    ignores: [
      'src/**/*.test.ts',
      'src/**/*.test.tsx',
      'src/**/*.test-fixture.ts',
      'src/**/*.test-fixture.tsx',
    ],
    plugins: housekeepingPlugin,
    rules: {
      complexity: ['error', 10],
      'housekeeping/no-nested-if': 'error',
    },
  },
  {
    files: [
      'src/**/*.test.ts',
      'src/**/*.test.tsx',
      'src/**/*.test-fixture.ts',
      'src/**/*.test-fixture.tsx',
    ],
    plugins: housekeepingPlugin,
    rules: {
      complexity: ['error', 20],
      'housekeeping/no-nested-if': 'error',
    },
  },
  {
    languageOptions: {
      parserOptions: {
        projectService: true,
        tsconfigRootDir: import.meta.dirname,
      },
    },
    rules: {
      '@typescript-eslint/no-unused-vars': [
        'error',
        { argsIgnorePattern: '^_', varsIgnorePattern: '^_' },
      ],
    },
  }
);
