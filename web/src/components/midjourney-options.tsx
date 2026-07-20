import * as Popover from '@radix-ui/react-popover'
import { ChevronDown, Layers3 } from 'lucide-react'

import type { MidjourneyOptions } from '#/lib/api'

export function MidjourneyOptionsControl({
  value,
  hasReference,
  onChange,
}: {
  value: MidjourneyOptions
  hasReference: boolean
  onChange: (value: MidjourneyOptions) => void
}) {
  const update = (patch: Partial<MidjourneyOptions>) =>
    onChange({ ...value, ...patch })
  const isV7 = value.version === '7'
  const title = isV7 ? 'V7 · 参数' : `V8.1 · ${value.resolution?.toUpperCase()}`

  return (
    <Popover.Root>
      <Popover.Trigger className="select-control mj-trigger">
        <Layers3 size={14} />
        {title}
        <ChevronDown className="select-chevron" size={13} />
      </Popover.Trigger>
      <Popover.Portal>
        <Popover.Content
          className="mj-options"
          side="top"
          align="start"
          sideOffset={8}
          collisionPadding={12}
        >
          <div className="mj-options-heading">
            <div>
              <strong>Midjourney 参数</strong>
              <span>服务端将校验并按固定顺序提交</span>
            </div>
            <div className="segmented-control" aria-label="Midjourney 版本">
              {(['8.1', '7'] as const).map((version) => (
                <button
                  type="button"
                  key={version}
                  data-active={value.version === version}
                  onClick={() =>
                    update(
                      version === '8.1'
                        ? {
                            version,
                            resolution: 'sd',
                            speed: 'fast',
                            quality: undefined,
                            draft: false,
                          }
                        : {
                            version,
                            resolution: undefined,
                            speed: 'fast',
                            quality: 1,
                            draft: false,
                          },
                    )
                  }
                >
                  V{version}
                </button>
              ))}
            </div>
          </div>

          <div className="mj-option-grid">
            {!isV7 && (
              <OptionSelect
                label="分辨率"
                value={value.resolution ?? 'sd'}
                options={[
                  ['sd', 'SD · 1024'],
                  ['hd', 'HD · 2048'],
                ]}
                onChange={(resolution) =>
                  update({ resolution: resolution as 'sd' | 'hd' })
                }
              />
            )}
            {isV7 && (
              <>
                <OptionSelect
                  label="速度"
                  value={value.speed}
                  options={[
                    ['fast', 'Fast'],
                    ['turbo', 'Turbo'],
                  ]}
                  onChange={(speed) =>
                    update({ speed: speed as 'fast' | 'turbo' })
                  }
                />
                <label className="option-toggle">
                  <span>Draft</span>
                  <input
                    type="checkbox"
                    checked={value.draft}
                    onChange={(event) =>
                      update({
                        draft: event.target.checked,
                        quality: event.target.checked
                          ? undefined
                          : (value.quality ?? 1),
                      })
                    }
                  />
                </label>
                {!value.draft && (
                  <OptionSelect
                    label="Quality"
                    value={String(value.quality ?? 1)}
                    options={[
                      ['1', '1'],
                      ['2', '2'],
                      ['4', '4'],
                    ]}
                    onChange={(quality) =>
                      update({ quality: Number(quality) as 1 | 2 | 4 })
                    }
                  />
                )}
              </>
            )}
            <RangeOption
              label="Stylize"
              value={value.stylize}
              max={1000}
              onChange={(stylize) => update({ stylize })}
            />
            <RangeOption
              label="Chaos"
              value={value.chaos}
              max={100}
              onChange={(chaos) => update({ chaos })}
            />
            <RangeOption
              label="Weird"
              value={value.weird}
              max={3000}
              onChange={(weird) => update({ weird })}
            />
            {hasReference && (
              <RangeOption
                label="Image Weight"
                value={value.image_weight ?? 1}
                max={3}
                step={0.1}
                onChange={(image_weight) => update({ image_weight })}
              />
            )}
          </div>
          <div className="mj-flags">
            <OptionFlag
              label="Raw"
              checked={value.raw}
              onChange={(raw) => update({ raw })}
            />
            <OptionFlag
              label="Tile"
              checked={value.tile}
              onChange={(tile) => update({ tile })}
            />
          </div>
        </Popover.Content>
      </Popover.Portal>
    </Popover.Root>
  )
}

function OptionSelect({
  label,
  value,
  options,
  onChange,
}: {
  label: string
  value: string
  options: [string, string][]
  onChange: (value: string) => void
}) {
  return (
    <label className="mj-option-select">
      <span>{label}</span>
      <select value={value} onChange={(event) => onChange(event.target.value)}>
        {options.map(([option, title]) => (
          <option key={option} value={option}>
            {title}
          </option>
        ))}
      </select>
    </label>
  )
}

function RangeOption({
  label,
  value,
  max,
  step = 1,
  onChange,
}: {
  label: string
  value: number
  max: number
  step?: number
  onChange: (value: number) => void
}) {
  return (
    <label className="mj-range">
      <span>
        {label} <output>{value}</output>
      </span>
      <input
        type="range"
        min="0"
        max={max}
        step={step}
        value={value}
        onChange={(event) => onChange(Number(event.target.value))}
      />
    </label>
  )
}

function OptionFlag({
  label,
  checked,
  onChange,
}: {
  label: string
  checked: boolean
  onChange: (checked: boolean) => void
}) {
  return (
    <label className="option-flag">
      <input
        type="checkbox"
        checked={checked}
        onChange={(event) => onChange(event.target.checked)}
      />
      {label}
    </label>
  )
}
