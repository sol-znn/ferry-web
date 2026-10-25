import {defineConfig} from 'eslint/config'
import pluginVue from 'eslint-plugin-vue'
import tseslint from 'typescript-eslint'
import vueParser from 'vue-eslint-parser'

// This project uses syntax-only TypeScript rules and TypeScript Vue scripts.
// Compose their supported flat presets directly, without filesystem discovery.
export default defineConfig(
  {
    name: 'app/files-to-ignore',
    ignores: ['**/dist/**', 'node_modules/**', 'public/wasm_exec.js', 'public/ferry.wasm'],
  },
  {
    name: 'app/files-to-lint',
    files: ['**/*.{ts,mts,tsx,vue}'],
    extends: [
      ...tseslint.configs.recommended.map((config) => ({
        ...config,
        files: ['**/*.{ts,mts,tsx,vue}'],
      })),
      pluginVue.configs['flat/essential'],
    ],
    languageOptions: {
      parserOptions: {
        parser: tseslint.parser,
        ecmaVersion: 2024,
        extraFileExtensions: ['.vue'],
      },
    },
    rules: {
      'vue/multi-word-component-names': 'off',
      '@typescript-eslint/no-explicit-any': 'warn',
      'no-console': ['warn', {allow: ['warn', 'error']}],
      'vue/no-v-html': 'warn',
      '@typescript-eslint/no-non-null-assertion': 'warn',
    },
  },
  {
    name: 'app/vue-typescript',
    files: ['**/*.vue'],
    languageOptions: {parser: vueParser},
    rules: {
      'vue/block-lang': ['error', {script: {lang: ['ts'], allowNoLang: false}}],
    },
  },
)
