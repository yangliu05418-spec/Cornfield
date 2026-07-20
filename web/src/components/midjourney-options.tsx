import * as Popover from '@radix-ui/react-popover'
import { ChevronDown, Layers3 } from 'lucide-react'

import type { MidjourneyOptions } from '#/lib/api'

export function MidjourneyOptionsControl({
  value,
  versions,
  hasReference,
  onChange,
}: {
  value: MidjourneyOptions
  versions: string[]
  hasReference: boolean
  onChange: (value: MidjourneyOptions) => void
}) {
  const update = (patch: Partial<MidjourneyOptions>) =>
    onChange({ ...value, ...patch })
  const supportsResolution = value.version === '8' || value.version === '8.1'
  const supportsDraft = value.version === '7'
  const supportsTurbo = !supportsResolution
  const qualityOptions = qualitiesFor(value.version)
  const title = supportsResolution
    ? `V${value.version} · ${value.resolution?.toUpperCase()}`
    : value.version === 'niji 6'
      ? 'Niji 6 · 参数'
      : `V${value.version} · 参数`

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
              {versions.map((version) => (
                <button
                  type="button"
                  key={version}
                  data-active={value.version === version}
                  onClick={() => update(optionsForVersion(version))}
                >
                  {version === 'niji 6' ? 'Niji 6' : `V${version}`}
                </button>
              ))}
            </div>
          </div>

          <div className="mj-option-grid">
            {supportsResolution && (
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
            {supportsTurbo && (
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
            )}
            {supportsDraft && (
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
            )}
            {qualityOptions.length > 0 && !value.draft && (
              <OptionSelect
                label="Quality"
                value={String(value.quality ?? 1)}
                options={qualityOptions.map((quality) => [
                  String(quality),
                  String(quality),
                ])}
                onChange={(quality) =>
                  update({ quality: Number(quality) as 0.5 | 1 | 2 | 4 })
                }
              />
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

function optionsForVersion(version: string): Partial<MidjourneyOptions> {
  const typedVersion = version as MidjourneyOptions['version']
  if (version === '8.1')
    return {
      version: typedVersion,
      resolution: 'sd',
      speed: 'fast',
      quality: undefined,
      draft: false,
    }
  if (version === '8')
    return {
      version: typedVersion,
      resolution: 'sd',
      speed: 'fast',
      quality: 1,
      draft: false,
    }
  return {
    version: typedVersion,
    resolution: undefined,
    speed: 'fast',
    quality: 1,
    draft: false,
  }
}

function qualitiesFor(version: MidjourneyOptions['version']) {
  if (version === '8.1') return []
  if (version === '8') return [1, 4] as const
  if (version === '7') return [1, 2, 4] as const
  return [0.5, 1, 2] as const
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
