// icons.test.tsx —— 图标集：已知名称渲染 svg，未知名称返回 null
import { describe, expect, it } from 'vitest'
import { render } from '@testing-library/react'
import Icon from '../lib/icons'

describe('Icon', () => {
  it('已知名称渲染 svg，且尺寸/类名可覆盖', () => {
    const { container } = render(<Icon name="doc" size={16} className="my-ic" />)
    const svg = container.querySelector('svg')
    expect(svg).toBeTruthy()
    expect(svg?.getAttribute('width')).toBe('16')
    expect(svg?.getAttribute('class')).toBe('my-ic')
  })

  it('默认类名为 ic，size 未传时不输出 width/height', () => {
    const { container } = render(<Icon name="sun" />)
    const svg = container.querySelector('svg')
    expect(svg?.getAttribute('class')).toBe('ic')
    expect(svg?.getAttribute('width')).toBeNull()
  })

  it('未知名称返回 null', () => {
    const { container } = render(<Icon name="no-such-icon" />)
    expect(container.querySelector('svg')).toBeNull()
    expect(container.textContent).toBe('')
  })
})
