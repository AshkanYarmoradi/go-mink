import assert from 'node:assert/strict';
import {readFile} from 'node:fs/promises';

const examplesSource = await readFile(new URL('../src/data/examples.ts', import.meta.url), 'utf8');
const displayed = [...examplesSource.matchAll(/id: "(append|replay|postgres)"[\s\S]*?code: `([\s\S]*?)`/g)];
assert.equal(displayed.length, 3, 'All three homepage examples must be checked');

const normalize = (code) => code.replaceAll('\r\n', '\n').replaceAll('\t', '    ').trim();
for (const [, id, code] of displayed) {
  const source = await readFile(new URL(`quickstart/${id}.go`, import.meta.url), 'utf8');
  const start = `// snippet:${id}:start`;
  const end = `// snippet:${id}:end`;
  assert.ok(source.includes(start) && source.includes(end), `${id}: source markers exist`);
  const excerpt = source.split(start)[1].split(end)[0];
  assert.equal(normalize(code), normalize(excerpt), `${id}: displayed code matches compiled source`);
}
console.log('All three homepage snippets match their compiled Go functions.');
