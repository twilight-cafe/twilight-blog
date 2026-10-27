// page-param.test.tsx —— 页码进 URL 的解析与写回
//
// 这里覆盖两条容易回归的边界：
//   1. URL 里的垃圾页码（0 / 负数 / 小数 / 非数字）必须安静地回第 1 页；
//   2. 翻页只改 page，不能把搜索的 ?q= 或标签的 ?id= 一起冲掉。

import { describe, expect, it } from 'vitest'
import { fireEvent, render, screen } from '@testing-library/react'
import { MemoryRouter, useLocation } from 'react-router-dom'
import { parsePageParam, usePageParam } from '../lib/page-param'

function Probe() {
  const [page, setPage] = usePageParam()
  const location = useLocation()
  return (
    <div>
      <span data-testid="page">{page}</span>
      <span data-testid="search">{location.search}</span>
      <button type="button" onClick={() => setPage(2)}>go-two</button>
      <button type="button" onClick={() => setPage(3)}>go-three</button>
      <button type="button" onClick={() => setPage(1)}>go-first</button>
    </div>
  )
}

function renderAt(route: string) {
  return render(
    <MemoryRouter initialEntries={[route]}>
      <Probe />
    </MemoryRouter>,
  )
}

describe('parsePageParam', () => {
  it('缺失与空串回第 1 页', () => {
    expect(parsePageParam(null)).toBe(1)
    expect(parsePageParam('')).toBe(1)
    expect(parsePageParam('   ')).toBe(1)
  })

  it('正整数原样采用（允许首尾空白）', () => {
    expect(parsePageParam('2')).toBe(2)
    expect(parsePageParam(' 12 ')).toBe(12)
  })

  it('0、负数、小数与非数字一律回第 1 页', () => {
    expect(parsePageParam('0')).toBe(1)
    expect(parsePageParam('-3')).toBe(1)
    expect(parsePageParam('1.5')).toBe(1)
    expect(parsePageParam('abc')).toBe(1)
    expect(parsePageParam('NaN')).toBe(1)
  })
})

describe('usePageParam', () => {
  it('从 URL 恢复页码，翻页写回且保留其他查询参数', () => {
    renderAt('/search?q=go&page=2')
    expect(screen.getByTestId('page').textContent).toBe('2')

    // 写成当前页码不产生导航：URL 保持不变。
    fireEvent.click(screen.getByRole('button', { name: 'go-two' }))
    expect(screen.getByTestId('search').textContent).toBe('?q=go&page=2')

    fireEvent.click(screen.getByRole('button', { name: 'go-three' }))
    expect(screen.getByTestId('page').textContent).toBe('3')
    const search = screen.getByTestId('search').textContent ?? ''
    expect(search).toContain('q=go')
    expect(search).toContain('page=3')

    // 回到第 1 页时删掉参数：列表首页的地址保持干净。
    fireEvent.click(screen.getByRole('button', { name: 'go-first' }))
    expect(screen.getByTestId('page').textContent).toBe('1')
    expect(screen.getByTestId('search').textContent).toBe('?q=go')
  })

  it('URL 里的非法页码按第 1 页处理，不抛错', () => {
    renderAt('/?page=oops')
    expect(screen.getByTestId('page').textContent).toBe('1')
  })
})
