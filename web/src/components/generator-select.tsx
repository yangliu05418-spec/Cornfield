import * as Select from '@radix-ui/react-select'
import { Check, ChevronDown } from 'lucide-react'
import type { ReactNode } from 'react'

export function GeneratorSelect({
  label,
  value,
  items,
  icon,
  onChange,
}: {
  label: string
  value: string
  items: { value: string; label: string }[]
  icon: ReactNode
  onChange: (value: string) => void
}) {
  return (
    <Select.Root value={value} onValueChange={onChange}>
      <Select.Trigger className="select-control" aria-label={label}>
        {icon}
        <Select.Value />
        <Select.Icon className="select-chevron">
          <ChevronDown size={13} />
        </Select.Icon>
      </Select.Trigger>
      <Select.Portal>
        <Select.Content
          className="generator-select-content"
          position="popper"
          side="top"
          sideOffset={8}
          collisionPadding={12}
        >
          <Select.Viewport className="generator-select-viewport">
            {items.map((item) => (
              <Select.Item
                className="generator-select-item"
                key={item.value}
                value={item.value}
              >
                <Select.ItemText>{item.label}</Select.ItemText>
                <Select.ItemIndicator>
                  <Check size={13} />
                </Select.ItemIndicator>
              </Select.Item>
            ))}
          </Select.Viewport>
        </Select.Content>
      </Select.Portal>
    </Select.Root>
  )
}
