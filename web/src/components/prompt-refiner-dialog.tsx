import { useEffect, useId, useMemo, useRef, useState } from 'react'

import type { PromptRefineResponse } from '#/lib/api'

type Selection = { start: number; end: number }

export function applyPromptRefinements(
  result: PromptRefineResponse,
  selected: ReadonlySet<string>,
  choices: Readonly<Record<string, string>>,
  selection: Selection,
): { prompt: string; selection: Selection } {
  const replacementFor = (findingID: string | undefined, text: string) =>
    findingID && selected.has(findingID) ? (choices[findingID] ?? text) : text
  const mapPosition = (position: number) => {
    let sourceOffset = 0
    let targetOffset = 0
    for (const segment of result.segments) {
      const text = replacementFor(segment.finding_id, segment.text)
      const sourceEnd = sourceOffset + segment.text.length
      if (position <= sourceEnd) {
        if (text !== segment.text && position > sourceOffset)
          return targetOffset + text.length
        return targetOffset + Math.max(0, position - sourceOffset)
      }
      sourceOffset = sourceEnd
      targetOffset += text.length
    }
    return targetOffset
  }

  const prompt = result.segments
    .map((segment) => replacementFor(segment.finding_id, segment.text))
    .join('')
  const nextStart = mapPosition(selection.start)
  const nextEnd = mapPosition(selection.end)
  return {
    prompt,
    selection: {
      start: Math.max(0, Math.min(nextStart, prompt.length)),
      end: Math.max(0, Math.min(nextEnd, prompt.length)),
    },
  }
}

export function PromptRefinerDialog({
  open,
  modelName,
  result,
  selection,
  onClose,
  onApply,
}: {
  open: boolean
  modelName: string
  result: PromptRefineResponse | null
  selection: Selection
  onClose: () => void
  onApply: (prompt: string, selection: Selection, count: number) => void
}) {
  const ref = useRef<HTMLDialogElement>(null)
  const titleID = useId()
  const [selected, setSelected] = useState<Set<string>>(new Set())
  const [choices, setChoices] = useState<Record<string, string>>({})

  useEffect(() => {
    const dialog = ref.current
    if (!dialog) return
    if (open && !dialog.open) dialog.showModal()
    if (!open && dialog.open) dialog.close()
  }, [open])

  useEffect(() => {
    if (!open || !result) return
    setSelected(new Set())
    setChoices(
      Object.fromEntries(
        result.findings
          .filter((item) => item.replacements?.length)
          .map((item) => [item.id, item.replacements![0]]),
      ),
    )
  }, [open, result])

  const locales = useMemo(() => {
    const values = new Set(result?.findings.map((item) => item.locale) ?? [])
    if (values.size === 0) return '未识别到风险语言'
    if (values.size > 1) return '中英混合'
    return values.has('zh') ? '中文' : '英文'
  }, [result])

  const apply = () => {
    if (!result) return
    const next = applyPromptRefinements(result, selected, choices, selection)
    onApply(next.prompt, next.selection, selected.size)
  }

  return (
    <dialog
      ref={ref}
      className="prompt-refiner-dialog"
      aria-labelledby={titleID}
      onCancel={(event) => {
        event.preventDefault()
        onClose()
      }}
      onClose={() => {
        if (open) onClose()
      }}
    >
      <header>
        <div>
          <p className="eyebrow">PROMPT CHECK · {result?.policy_version}</p>
          <h2 id={titleID}>检查并优化提示词</h2>
        </div>
        <p className="prompt-refiner-context">
          {modelName} · {locales}
        </p>
      </header>

      {result && (
        <div className="prompt-refiner-content">
          <section
            className="prompt-refiner-original"
            aria-label="提示词检查结果"
          >
            {result.segments.map((segment, index) =>
              segment.finding_id ? (
                <mark key={`${segment.finding_id}-${index}`}>
                  {segment.text}
                </mark>
              ) : (
                <span key={index}>{segment.text}</span>
              ),
            )}
          </section>

          {result.diagnostics.length > 0 && (
            <section className="prompt-refiner-diagnostics">
              {result.diagnostics.map((item) => (
                <div key={item.code}>
                  <span>诊断</span>
                  <p>{item.message}</p>
                  {item.used && item.limit && (
                    <small>
                      {item.used} / {item.limit} 字符
                    </small>
                  )}
                </div>
              ))}
            </section>
          )}

          {result.findings.length > 0 ? (
            <section className="prompt-refiner-findings">
              {result.findings.map((finding) => {
                const canApply = Boolean(finding.replacements?.length)
                return (
                  <article key={finding.id}>
                    <label className="prompt-refiner-finding-title">
                      <input
                        type="checkbox"
                        disabled={!canApply}
                        checked={selected.has(finding.id)}
                        onChange={(event) => {
                          setSelected((current) => {
                            const next = new Set(current)
                            if (event.target.checked) next.add(finding.id)
                            else next.delete(finding.id)
                            return next
                          })
                        }}
                      />
                      <span>
                        <strong>{finding.original}</strong>
                        <small>{finding.category}</small>
                      </span>
                    </label>
                    <p>{finding.reason}</p>
                    {canApply ? (
                      <div className="prompt-refiner-choices">
                        {finding.replacements!.map((replacement) => (
                          <label key={replacement}>
                            <input
                              type="radio"
                              name={`replacement-${finding.id}`}
                              value={replacement}
                              checked={choices[finding.id] === replacement}
                              onChange={() =>
                                setChoices((current) => ({
                                  ...current,
                                  [finding.id]: replacement,
                                }))
                              }
                            />
                            <span>{replacement}</span>
                          </label>
                        ))}
                      </div>
                    ) : (
                      <p className="prompt-refiner-manual">
                        请手动修改这一处表达
                      </p>
                    )}
                  </article>
                )
              })}
            </section>
          ) : (
            result.diagnostics.length === 0 && (
              <p className="prompt-refiner-clean">
                未发现需要处理的内容，可以保持原文继续创作。
              </p>
            )
          )}
        </div>
      )}

      <footer>
        <button type="button" className="secondary-button" onClick={onClose}>
          保持原文
        </button>
        <button
          type="button"
          className="primary-button"
          disabled={!result || selected.size === 0}
          onClick={apply}
        >
          应用所选修改
        </button>
      </footer>
    </dialog>
  )
}
