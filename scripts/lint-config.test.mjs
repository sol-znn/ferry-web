// These fixtures ensure the dependency change retains active TypeScript and
// Vue correctness checks, including TypeScript expressions in Vue templates.
import assert from 'node:assert/strict'
import {createRequire} from 'node:module'
import {dirname, resolve} from 'node:path'
import {fileURLToPath} from 'node:url'
import test from 'node:test'

const ui = resolve(dirname(fileURLToPath(import.meta.url)), '../ui')
const require = createRequire(resolve(ui, 'package.json'))
const {ESLint} = require('eslint')
const eslint = new ESLint({cwd: ui})
async function lint(source, file) {
  const [result] = await eslint.lintText(source, {filePath: resolve(ui, 'src', file)})
  return result.messages
}
test('valid TypeScript is parsed and checked', async () => {
  assert.deepEqual(await lint('export const value: number = 1\n', 'fixture.ts'), [])
})
test('unused TypeScript variables still produce an error', async () => {
  const messages = await lint('const unused: number = 1\nexport {}\n', 'fixture.ts')
  assert.ok(messages.some((m) => m.ruleId === '@typescript-eslint/no-unused-vars' && m.severity === 2))
})
test('explicit any retains its configured warning', async () => {
  const messages = await lint('export const value: any = 1\n', 'fixture.ts')
  assert.ok(messages.some((m) => m.ruleId === '@typescript-eslint/no-explicit-any' && m.severity === 1))
})
test('Vue scripts and TypeScript template expressions are parsed together', async () => {
  const messages = await lint(
    '<script setup lang="ts">const value: number = 1</script><template><div>{{ (value as number).toFixed() }}</div></template>',
    'Fixture.vue',
  )
  assert.deepEqual(messages, [])
})
test('unused variables in Vue scripts still produce an error', async () => {
  const messages = await lint(
    '<script setup lang="ts">const unused: number = 1</script><template><div>fixture</div></template>',
    'Fixture.vue',
  )
  assert.ok(messages.some((m) => m.ruleId === '@typescript-eslint/no-unused-vars' && m.severity === 2))
})
test('Vue essential rules remain active', async () => {
  const messages = await lint('<template><div v-for="value in [1, 2]">{{ value }}</div></template>', 'Fixture.vue')
  assert.ok(messages.some((m) => m.ruleId === 'vue/require-v-for-key' && m.severity === 2))
})
test('Vue scripts must still declare TypeScript', async () => {
  const messages = await lint('<script setup>const value = 1</script><template>{{ value }}</template>', 'Fixture.vue')
  assert.ok(messages.some((m) => m.ruleId === 'vue/block-lang' && m.severity === 2))
})
test('generated Go runtime files remain ignored', async () => {
  assert.equal(await eslint.isPathIgnored(resolve(ui, 'public/wasm_exec.js')), true)
})

test('TypeScript base rules also apply inside Vue scripts', async () => {
  const messages = await lint(
    '<script setup lang="ts">let value: number = 1</script><template>{{ value }}</template>',
    'Fixture.vue',
  )
  assert.ok(messages.some((m) => m.ruleId === 'prefer-const' && m.severity === 2))
})
test('TypeScript base rules remain active in standalone TypeScript', async () => {
  const messages = await lint('export function read() { let value: number = 1; return value }\n', 'fixture.ts')
  assert.ok(messages.some((m) => m.ruleId === 'prefer-const' && m.severity === 2))
})
