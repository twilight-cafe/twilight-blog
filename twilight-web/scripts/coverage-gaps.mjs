// 列出 coverage-final.json 里每个文件的未覆盖语句/函数/分支位置。
// 用法：node scripts/coverage-gaps.mjs [coverage目录]
import { readFileSync } from 'node:fs'
import { join } from 'node:path'

const dir = process.argv[2] ?? 'coverage'
const data = JSON.parse(readFileSync(join(dir, 'coverage-final.json'), 'utf8'))

for (const [file, cov] of Object.entries(data)) {
  const short = file.replace(/^.*twilight-web[\\/]/, '')
  const gaps = []

  // 未覆盖语句
  for (const [id, count] of Object.entries(cov.s)) {
    if (count === 0) {
      const loc = cov.statementMap[id]
      gaps.push(`stmt L${loc.start.line}`)
    }
  }
  // 未覆盖函数
  for (const [id, count] of Object.entries(cov.f)) {
    if (count === 0) {
      const loc = cov.fnMap[id]
      gaps.push(`fn L${loc.decl.start.line} ${loc.name}`)
    }
  }
  // 未覆盖分支：标出具体走了哪一边没走（1=consequent/前序，2=alternate/后序）
  for (const [id, counts] of Object.entries(cov.b)) {
    const loc = cov.branchMap[id]
    const missed = counts.map((c, i) => (c === 0 ? i + 1 : 0)).filter(Boolean)
    if (missed.length) {
      const label = loc.type === 'if' ? ['if-true', 'if-false'] : loc.type === 'binary-expr' ? ['left', 'right'] : [0, 1].map((i) => `path${i + 1}`)
      gaps.push(`branch L${loc.loc.start.line} ${loc.type} missed:${missed.map((i) => label[i] ?? `path${i}`).join(',')}`)
    }
  }

  if (gaps.length) {
    console.log(`\n== ${short}`)
    for (const g of gaps.sort()) console.log('  ' + g)
  }
}
