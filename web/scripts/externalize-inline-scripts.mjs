import { createHash } from 'node:crypto'
import {
  existsSync,
  mkdirSync,
  readdirSync,
  readFileSync,
  writeFileSync,
} from 'node:fs'
import { extname, join, resolve } from 'node:path'

const clientRoot = resolve('dist/client')
const assetRoot = join(clientRoot, 'assets')

if (!existsSync(clientRoot)) {
  throw new Error(`Static client output is missing: ${clientRoot}`)
}

mkdirSync(assetRoot, { recursive: true })

const htmlFiles = walk(clientRoot).filter((file) => extname(file) === '.html')
let externalized = 0

for (const file of htmlFiles) {
  const source = readFileSync(file, 'utf8')
  const output = source.replace(
    /<script\b([^>]*)>([\s\S]*?)<\/script>/gi,
    (full, attributes, body) => {
      if (/\bsrc\s*=/i.test(attributes) || !body.trim()) return full

      const digest = createHash('sha256')
        .update(body)
        .digest('hex')
        .slice(0, 20)
      const filename = `inline-${digest}.js`
      const target = join(assetRoot, filename)
      if (!existsSync(target)) writeFileSync(target, body)
      externalized += 1
      return `<script${attributes} src="/assets/${filename}"></script>`
    },
  )

  const remainingInlineScripts = [
    ...output.matchAll(/<script\b([^>]*)>([\s\S]*?)<\/script>/gi),
  ].filter(
    ([, attributes, body]) => !/\bsrc\s*=/i.test(attributes) && body.trim(),
  )
  if (remainingInlineScripts.length) {
    throw new Error(`CSP hardening failed: inline script remains in ${file}`)
  }
  writeFileSync(file, output)
}

if (!htmlFiles.length || !externalized) {
  throw new Error(
    'CSP hardening failed: no static HTML bootstrap scripts were found',
  )
}

console.log(
  `Externalized ${externalized} inline scripts across ${htmlFiles.length} HTML files`,
)

function walk(directory) {
  return readdirSync(directory, { withFileTypes: true }).flatMap((entry) => {
    const path = join(directory, entry.name)
    return entry.isDirectory() ? walk(path) : [path]
  })
}
