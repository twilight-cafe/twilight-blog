// translate-ui.test.tsx —— 编辑器 AI 翻译按钮 与 站点设置里的翻译配置卡
//
// 两个界面共享同一条契约：密钥永远不回显，输入框留空表示「保持已保存的值」。
// 因此这里既验证「调用参数正确」，也验证「空密钥不会出现在请求体里」。

import { beforeEach, describe, expect, it, vi } from 'vitest'
import { act, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { MemoryRouter, Route, Routes } from 'react-router-dom'

import Editor from '../pages/admin/Editor'
import SiteSettings from '../pages/admin/SiteSettings'
import { DialogProvider } from '../lib/dialog'
import { ToastProvider } from '../lib/toast'
import { resetSiteCacheForTests } from '../lib/site'
import { resetLocaleForTests } from '../lib/i18n'
import { adminMe, getSharedClientMock, resetClientMock } from './helpers'

vi.mock('../lib/client', async () => {
  const { getSharedClientMock: shared } = await import('./helpers')
  return shared()
})

const client = getSharedClientMock()

const ADMIN_SITE = {
  description: '测试站',
  avatar_url: '',
  code_block_max_lines: 20,
  home_page_size: 12,
  border_radius: 4,
  post_banner_height: 400,
  moments_enabled: true,
  comment_notify_to: '',
}

function translateConfig(overrides: Partial<Record<string, unknown>> = {}) {
  return {
    enabled: true,
    url: 'https://api.example.com/v1',
    model: 'gpt-test',
    timeout_seconds: 60,
    max_tokens: 4096,
    prompt_template: '',
    thinking_effort: '',
    api_key_configured: true,
    configured: true,
    ...overrides,
  }
}

beforeEach(() => {
  resetClientMock(client)
  resetSiteCacheForTests()
  resetLocaleForTests()
  client.setCurrentUser(adminMe)
  client.blog.getSite.mockResolvedValue({ description: '测试站' })
  client.blog.getAdminSite.mockResolvedValue(ADMIN_SITE)
})

function renderEditor(route = '/admin/editor') {
  return render(
    <MemoryRouter initialEntries={[route]}>
      <ToastProvider>
        <DialogProvider>
          <Routes>
            <Route path="/admin/editor/:id" element={<Editor />} />
            <Route path="/admin/editor" element={<Editor />} />
            <Route path="/admin/posts" element={<div>POSTS-LIST</div>} />
          </Routes>
        </DialogProvider>
      </ToastProvider>
    </MemoryRouter>,
  )
}

function renderSettings(route = '/admin/site-settings?tab=translate') {
  return render(
    <MemoryRouter initialEntries={[route]}>
      <ToastProvider>
        <DialogProvider>
          <SiteSettings />
        </DialogProvider>
      </ToastProvider>
    </MemoryRouter>,
  )
}

function editorTitle(container: HTMLElement): HTMLInputElement {
  return container.querySelector('.ed-title input, input.ed-title') as HTMLInputElement
}

function editorBody(container: HTMLElement): HTMLTextAreaElement {
  return container.querySelector('textarea') as HTMLTextAreaElement
}

/** AI 翻译页签下只有一张表单（站点设置表单未渲染）。 */
function translateForm(container: HTMLElement): HTMLFormElement {
  return container.querySelector('form') as HTMLFormElement
}

function saveTranslateButton(container: HTMLElement): HTMLButtonElement {
  return within(translateForm(container)).getByRole('button', { name: '保存翻译设置' }) as HTMLButtonElement
}

describe('Editor AI 翻译', () => {
  it('中文页签翻译成英文：调用参数正确、自动勾选 AI、切到英文页签', async () => {
    client.blog.translateArticle.mockResolvedValueOnce({ title: 'Hello', content: 'Body' })
    const { container } = renderEditor()

    fireEvent.change(editorTitle(container), { target: { value: '标题' } })
    fireEvent.change(editorBody(container), { target: { value: '正文' } })
    fireEvent.click(screen.getByText('AI 翻译为英文'))

    await waitFor(() =>
      expect(client.blog.translateArticle).toHaveBeenCalledWith({
        title: '标题',
        content: '正文',
        from: 'zh',
        to: 'en',
      }),
    )
    await waitFor(() => expect(editorTitle(container).value).toBe('Hello'))
    expect(editorBody(container).value).toBe('Body')
    // 只有目标语言（英文）被标为 AI；中文原文仍是非 AI。
    const [aiZH, aiEN] = screen.getAllByRole('checkbox') as HTMLInputElement[]
    expect(aiEN.checked).toBe(true)
    expect(aiZH.checked).toBe(false)
    expect(screen.getByRole('tab', { name: 'English' }).getAttribute('aria-selected')).toBe('true')
    expect(screen.getByText('翻译完成，已切换到目标语言')).toBeTruthy()
  })

  it('英文页签翻译成中文：走 target=zh 分支并回填中文标题/正文', async () => {
    client.blog.translateArticle.mockResolvedValueOnce({ title: '你好', content: '正文' })
    const { container } = renderEditor()

    fireEvent.click(screen.getByRole('tab', { name: 'English' }))
    fireEvent.change(editorTitle(container), { target: { value: 'Hello' } })
    fireEvent.change(editorBody(container), { target: { value: 'Body' } })
    fireEvent.click(screen.getByText('AI 翻译为中文'))

    await waitFor(() =>
      expect(client.blog.translateArticle).toHaveBeenCalledWith({
        title: 'Hello',
        content: 'Body',
        from: 'en',
        to: 'zh',
      }),
    )
    await waitFor(() => expect(editorTitle(container).value).toBe('你好'))
    expect(editorBody(container).value).toBe('正文')
    const [aiZH, aiEN] = screen.getAllByRole('checkbox') as HTMLInputElement[]
    expect(aiZH.checked).toBe(true)
    expect(aiEN.checked).toBe(false)
    expect(screen.getByRole('tab', { name: '中文' }).getAttribute('aria-selected')).toBe('true')
  })

  it('目标页签已有内容时先确认；取消则不发请求', async () => {
    const { container } = renderEditor()
    fireEvent.change(editorTitle(container), { target: { value: '中文标题' } })
    // 先填英文页签，制造「目标非空」。
    fireEvent.click(screen.getByRole('tab', { name: 'English' }))
    fireEvent.change(editorTitle(container), { target: { value: 'Existing' } })
    fireEvent.click(screen.getByRole('tab', { name: '中文' }))

    fireEvent.click(screen.getByText('AI 翻译为英文'))
    await screen.findByText('覆盖目标语言？')
    fireEvent.click(screen.getByText('取消'))

    await waitFor(() => expect(screen.queryByText('覆盖目标语言？')).toBeNull())
    expect(client.blog.translateArticle).not.toHaveBeenCalled()
  })

  it('确认覆盖后调用翻译并用结果替换目标页签', async () => {
    client.blog.translateArticle.mockResolvedValueOnce({ title: 'Overwritten', content: 'New body' })
    const { container } = renderEditor()
    fireEvent.change(editorTitle(container), { target: { value: '中文标题' } })
    fireEvent.click(screen.getByRole('tab', { name: 'English' }))
    fireEvent.change(editorTitle(container), { target: { value: 'Existing' } })
    fireEvent.change(editorBody(container), { target: { value: 'Old body' } })
    fireEvent.click(screen.getByRole('tab', { name: '中文' }))

    fireEvent.click(screen.getByText('AI 翻译为英文'))
    await screen.findByText('覆盖目标语言？')
    fireEvent.click(screen.getByText('确定'))

    await waitFor(() => expect(client.blog.translateArticle).toHaveBeenCalledTimes(1))
    await waitFor(() => expect(editorTitle(container).value).toBe('Overwritten'))
    expect(editorBody(container).value).toBe('New body')
  })

  it('标题为空时提示且不发请求', async () => {
    const { container } = renderEditor()
    fireEvent.click(screen.getByText('AI 翻译为英文'))
    expect(await screen.findByText('请先输入标题再翻译')).toBeTruthy()
    expect(client.blog.translateArticle).not.toHaveBeenCalled()
    // 未翻译时两个语言的 AI 开关都保持原样。
    expect((screen.getAllByRole('checkbox') as HTMLInputElement[]).every((input) => !input.checked)).toBe(true)
    expect(editorTitle(container).value).toBe('')
  })

  it('翻译失败时显示错误且不修改任何内容', async () => {
    client.blog.translateArticle.mockRejectedValueOnce(new Error('上游炸了'))
    const { container } = renderEditor()
    fireEvent.change(editorTitle(container), { target: { value: '标题' } })
    fireEvent.click(screen.getByText('AI 翻译为英文'))

    expect(await screen.findByText('上游炸了')).toBeTruthy()
    expect(editorTitle(container).value).toBe('标题')
    expect((screen.getAllByRole('checkbox') as HTMLInputElement[]).every((input) => !input.checked)).toBe(true)
    expect(screen.getByRole('tab', { name: '中文' }).getAttribute('aria-selected')).toBe('true')
    // 错误对话框与按钮恢复同属一次渲染之后的两帧；并行跑整个套件时用 waitFor，
    // 避免把「动画还没结束」误报成「按钮没恢复」。
    await waitFor(() =>
      expect((screen.getByText('AI 翻译为英文') as HTMLButtonElement).disabled).toBe(false),
    )
  })

  it('翻译请求进行中禁用保存，避免先落库未回填的内容', async () => {
    let resolveTranslate: (value: { title: string; content: string }) => void = () => {}
    client.blog.translateArticle.mockReturnValueOnce(new Promise((resolve) => { resolveTranslate = resolve }))
    const { container } = renderEditor()
    fireEvent.change(editorTitle(container), { target: { value: '标题' } })
    fireEvent.click(screen.getByText('AI 翻译为英文'))

    await screen.findByText('翻译中…')
    expect(screen.getByRole('button', { name: '保存' }).hasAttribute('disabled')).toBe(true)

    await act(async () => { resolveTranslate({ title: 'Hello', content: 'Body' }) })
    await waitFor(() => expect(screen.getByRole('button', { name: '保存' }).hasAttribute('disabled')).toBe(false))
  })
})

describe('SiteSettings AI 翻译配置', () => {
  it('回显已保存配置：地址/模型/超时/上限与密钥状态，且不回显密钥', async () => {
    client.blog.getTranslateConfig.mockResolvedValue(translateConfig())
    const { container } = renderSettings()

    await waitFor(() => expect(within(translateForm(container)).getByDisplayValue('https://api.example.com/v1')).toBeTruthy())
    expect(within(translateForm(container)).getByDisplayValue('gpt-test')).toBeTruthy()
    expect(within(translateForm(container)).getByDisplayValue('60')).toBeTruthy()
    expect(within(translateForm(container)).getByDisplayValue('4096')).toBeTruthy()
    expect(within(translateForm(container)).getByPlaceholderText('已保存，留空保持不变')).toBeTruthy()
    expect(screen.getByText('已就绪')).toBeTruthy()
    expect(container.textContent).not.toContain('secret')
    expect(saveTranslateButton(container).disabled).toBe(true)
  })

  it('只改模型且密钥留空：请求体里没有 api_key', async () => {
    client.blog.getTranslateConfig.mockResolvedValue(translateConfig())
    client.blog.updateTranslateConfig.mockResolvedValue(translateConfig({ model: 'gpt-new' }))
    const { container } = renderSettings()
    await waitFor(() => expect(within(translateForm(container)).getByDisplayValue('gpt-test')).toBeTruthy())

    const form = translateForm(container)
    fireEvent.change(within(form).getByDisplayValue('gpt-test'), { target: { value: ' gpt-new ' } })
    const save = saveTranslateButton(container)
    expect(save.disabled).toBe(false)
    fireEvent.click(save)

    await waitFor(() => expect(client.blog.updateTranslateConfig).toHaveBeenCalledTimes(1))
    const payload = client.blog.updateTranslateConfig.mock.calls[0][0] as Record<string, unknown>
    expect(payload).toEqual({
      enabled: true,
      url: 'https://api.example.com/v1',
      model: 'gpt-new',
      timeout_seconds: 60,
      max_tokens: 4096,
      thinking_effort: '',
    })
    // 模板没改时不能把它写回去：否则「当前生效的默认模板」会被固化成
    // 一份自定义副本，内置模板后续升级就再也用不上了。
    expect('prompt_template' in payload).toBe(false)
    expect('api_key' in payload).toBe(false)
    expect(await screen.findByText('AI 翻译设置已更新')).toBeTruthy()
    expect(within(translateForm(container)).getByDisplayValue('gpt-new')).toBeTruthy()
  })

  it('首次配置：填入地址/模型/密钥后提交，key 随请求发送', async () => {
    const { container } = renderSettings()
    await waitFor(() => expect(within(translateForm(container)).getByPlaceholderText('https://api.openai.com/v1')).toBeTruthy())
    const form = translateForm(container)

    fireEvent.change(within(form).getByPlaceholderText('https://api.openai.com/v1'), {
      target: { value: ' https://api.example.com/v1 ' },
    })
    fireEvent.change(within(form).getByPlaceholderText('gpt-4o-mini'), { target: { value: 'gpt-test' } })
    fireEvent.change(within(form).getByPlaceholderText('sk-…'), { target: { value: ' sk-secret ' } })
    client.blog.updateTranslateConfig.mockResolvedValue(translateConfig())

    const save = saveTranslateButton(container)
    expect(save.disabled).toBe(false)
    fireEvent.click(save)

    await waitFor(() =>
      expect(client.blog.updateTranslateConfig).toHaveBeenCalledWith({
        enabled: true,
        url: 'https://api.example.com/v1',
        model: 'gpt-test',
        timeout_seconds: 60,
        max_tokens: 4096,
        thinking_effort: '',
        api_key: 'sk-secret',
      }),
    )
    expect((within(translateForm(container)).getByPlaceholderText(/已保存/) as HTMLInputElement).value).toBe('')
  })

  it('开关可独立保存，并标记未保存状态', async () => {
    client.blog.getTranslateConfig.mockResolvedValue(translateConfig())
    client.blog.updateTranslateConfig.mockResolvedValue(translateConfig({ enabled: false }))
    const { container } = renderSettings()
    await waitFor(() => expect(within(translateForm(container)).getByDisplayValue('gpt-test')).toBeTruthy())

    const form = translateForm(container)
    expect(within(form).getByText('已保存')).toBeTruthy()
    fireEvent.click(within(form).getByRole('checkbox'))
    expect(within(form).getByText('有未保存的修改')).toBeTruthy()
    // 关掉开关是「已停用」，不是「未配置完整」——配置仍是完整的，只是没开。
    expect(within(form).getByText('已停用')).toBeTruthy()
    expect(within(form).getByText(/AI 翻译已停用/)).toBeTruthy()
    fireEvent.click(saveTranslateButton(container))

    await waitFor(() => expect(client.blog.updateTranslateConfig).toHaveBeenCalledWith(expect.objectContaining({ enabled: false })))
    // 保存后服务端返回 configured=false（因为开关关着），界面仍应显示「已停用」。
    await waitFor(() => expect(within(translateForm(container)).getByText('已停用')).toBeTruthy())
  })

  it('非法 URL / 缺模型 / 越界数字都会禁用保存', async () => {
    client.blog.getTranslateConfig.mockResolvedValue(translateConfig())
    const { container } = renderSettings()
    await waitFor(() => expect(within(translateForm(container)).getByDisplayValue('gpt-test')).toBeTruthy())
    const form = translateForm(container)
    const urlInput = within(form).getByDisplayValue('https://api.example.com/v1')
    const modelInput = within(form).getByDisplayValue('gpt-test')
    const [timeoutInput, tokensInput] = within(form).getAllByRole('spinbutton')

    // 非 http(s)：提示出现且保存被禁用。
    fireEvent.change(urlInput, { target: { value: 'ftp://example.com' } })
    expect(screen.getByText(/地址必须以 http/)).toBeTruthy()
    expect(saveTranslateButton(container).disabled).toBe(true)

    // 地址合法但模型为空。
    fireEvent.change(urlInput, { target: { value: 'https://api.example.com/v1' } })
    fireEvent.change(modelInput, { target: { value: '   ' } })
    expect(saveTranslateButton(container).disabled).toBe(true)

    // 模型补回，超时越界。
    fireEvent.change(modelInput, { target: { value: 'gpt-test' } })
    fireEvent.change(timeoutInput, { target: { value: '1' } })
    expect(saveTranslateButton(container).disabled).toBe(true)

    // 超时合法，输出上限越界。
    fireEvent.change(timeoutInput, { target: { value: '60' } })
    fireEvent.change(tokensInput, { target: { value: '1' } })
    expect(saveTranslateButton(container).disabled).toBe(true)

    // 全部合法时按钮恢复可用（模型改回原值也算一次修改）。
    fireEvent.change(tokensInput, { target: { value: '4096' } })
    fireEvent.change(modelInput, { target: { value: 'gpt-test-2' } })
    expect(saveTranslateButton(container).disabled).toBe(false)

    // 小数不能被 parseInt 截成整数后放行：61.5 不是合法的超时值。
    fireEvent.change(timeoutInput, { target: { value: '61.5' } })
    expect(saveTranslateButton(container).disabled).toBe(true)
  })

  it('保存失败时保留输入并显示服务端错误', async () => {
    client.blog.getTranslateConfig.mockResolvedValue(translateConfig())
    client.blog.updateTranslateConfig.mockRejectedValueOnce(new Error('设置保存失败X'))
    const { container } = renderSettings()
    await waitFor(() => expect(within(translateForm(container)).getByDisplayValue('gpt-test')).toBeTruthy())
    const form = translateForm(container)
    fireEvent.change(within(form).getByDisplayValue('gpt-test'), { target: { value: 'gpt-broken' } })
    fireEvent.click(saveTranslateButton(container))

    expect(await screen.findByText('设置保存失败X')).toBeTruthy()
    expect(within(translateForm(container)).getByDisplayValue('gpt-broken')).toBeTruthy()
  })

  it('配置加载失败时保留默认值并禁用保存', async () => {
    client.blog.getTranslateConfig.mockRejectedValue(new Error('离线'))
    const { container } = renderSettings()

    await waitFor(() => expect(within(translateForm(container)).getByPlaceholderText('https://api.openai.com/v1')).toBeTruthy())
    expect(within(translateForm(container)).getByDisplayValue('60')).toBeTruthy()
    expect(within(translateForm(container)).getByDisplayValue('4096')).toBeTruthy()
    expect(screen.getByText('加载失败')).toBeTruthy()
    // 页面不会因为一次配置读取失败而卡住，但保存必须被禁用：
    // 此时表单里的空白只是默认值，不是服务端的真实配置。
    expect(saveTranslateButton(container).disabled).toBe(true)
  })

  it('提示词模板：回显已保存模板，编辑后随配置保存并回填服务端值', async () => {
    client.blog.getTranslateConfig.mockResolvedValue(translateConfig({ prompt_template: '内置 {{.Title}}' }))
    client.blog.updateTranslateConfig.mockResolvedValue(translateConfig({ prompt_template: '自定义 {{.TitleJSON}}' }))
    const { container } = renderSettings()
    const prompt = () => translateForm(container).querySelector('textarea') as HTMLTextAreaElement
    await waitFor(() => expect(prompt().value).toBe('内置 {{.Title}}'))

    fireEvent.change(prompt(), { target: { value: '  自定义 {{.TitleJSON}}  ' } })
    expect(screen.getAllByText('有未保存的修改').length).toBeGreaterThan(0)

    fireEvent.click(saveTranslateButton(container))
    await waitFor(() =>
      expect(client.blog.updateTranslateConfig).toHaveBeenCalledWith(
        expect.objectContaining({ prompt_template: '自定义 {{.TitleJSON}}' }),
      ),
    )
    // 首尾空白与保存请求一致地被裁掉。
    await waitFor(() => expect(prompt().value).toBe('自定义 {{.TitleJSON}}'))
  })

  it('清空模板并保存：下发空串以恢复内置默认模板', async () => {
    client.blog.getTranslateConfig.mockResolvedValue(translateConfig({ prompt_template: '自定义模板' }))
    client.blog.updateTranslateConfig.mockResolvedValue(translateConfig({ prompt_template: '内置默认\n' }))
    const { container } = renderSettings()
    const prompt = () => translateForm(container).querySelector('textarea') as HTMLTextAreaElement
    await waitFor(() => expect(prompt().value).toBe('自定义模板'))

    fireEvent.change(prompt(), { target: { value: '' } })
    fireEvent.click(saveTranslateButton(container))
    await waitFor(() =>
      expect(client.blog.updateTranslateConfig).toHaveBeenCalledWith(
        expect.objectContaining({ prompt_template: '' }),
      ),
    )
    // 服务端回的是当前生效的内置模板；末尾换行不应让页面再次变成「未保存」。
    await waitFor(() => expect(prompt().value).toBe('内置默认\n'))
    expect(within(translateForm(container)).getByText('已保存')).toBeTruthy()
  })

  it('默认模板末尾带换行时，加载后不应显示「有未保存的修改」', async () => {
    // 服务端下发的内置模板来自 prompt.tmpl，末尾有一个换行；而保存时会被
    // TrimSpace。比较必须同样裁掉首尾空白，否则刚打开页面就会误报未保存。
    client.blog.getTranslateConfig.mockResolvedValue(translateConfig({ prompt_template: '内置模板\n' }))
    const { container } = renderSettings()
    const prompt = () => translateForm(container).querySelector('textarea') as HTMLTextAreaElement
    await waitFor(() => expect(prompt().value).toBe('内置模板\n'))

    expect(within(translateForm(container)).getByText('已保存')).toBeTruthy()
    expect(within(translateForm(container)).queryByText('有未保存的修改')).toBeNull()
    expect(saveTranslateButton(container).disabled).toBe(true)
  })

  it('查询模型：成功后列出候选，点选回填模型并随保存下发', async () => {
    client.blog.getTranslateConfig.mockResolvedValue(translateConfig())
    client.blog.listTranslateModels.mockResolvedValue({ models: ['gpt-a', 'gpt-b'] })
    const { container } = renderSettings()
    const form = () => translateForm(container)
    await waitFor(() => expect(within(form()).getByDisplayValue('gpt-test')).toBeTruthy())

    fireEvent.click(within(form()).getByRole('button', { name: '查询模型' }))
    // 密钥未输入 → 请求体不带 api_key，由服务端沿用已保存的值。
    await waitFor(() => expect(client.blog.listTranslateModels).toHaveBeenCalledWith({ url: 'https://api.example.com/v1' }))
    expect(await screen.findByText('找到 2 个模型')).toBeTruthy()

    fireEvent.click(within(form()).getByRole('button', { name: 'gpt-b' }))
    expect(within(form()).getByDisplayValue('gpt-b')).toBeTruthy()

    client.blog.updateTranslateConfig.mockResolvedValue(translateConfig({ model: 'gpt-b' }))
    fireEvent.click(saveTranslateButton(container))
    await waitFor(() =>
      expect(client.blog.updateTranslateConfig).toHaveBeenCalledWith(expect.objectContaining({ model: 'gpt-b' })),
    )
  })

  it('查询模型：查询中禁用按钮，空结果与请求失败分别提示', async () => {
    client.blog.getTranslateConfig.mockResolvedValue(translateConfig())
    let release: (value: { models: string[] }) => void = () => {}
    client.blog.listTranslateModels.mockReturnValueOnce(new Promise((resolve) => { release = resolve }))
    const { container } = renderSettings()
    const form = () => translateForm(container)
    await waitFor(() => expect(within(form()).getByDisplayValue('gpt-test')).toBeTruthy())

    fireEvent.click(within(form()).getByRole('button', { name: '查询模型' }))
    const busy = await within(form()).findByRole('button', { name: '查询中…' })
    expect(busy.hasAttribute('disabled')).toBe(true)
    release({ models: [] })
    expect(await screen.findByText('该服务没有返回任何模型')).toBeTruthy()
    // 没有候选时不再渲染模型按钮。
    expect(within(form()).queryByRole('button', { name: 'gpt-a' })).toBeNull()

    client.blog.listTranslateModels.mockRejectedValueOnce(new Error('上游拒绝'))
    fireEvent.click(within(form()).getByRole('button', { name: '查询模型' }))
    expect(await screen.findByText('上游拒绝')).toBeTruthy()
  })

  it('查询模型：地址非法时只提示，不发请求', async () => {
    client.blog.getTranslateConfig.mockResolvedValue(translateConfig())
    const { container } = renderSettings()
    const form = () => translateForm(container)
    await waitFor(() => expect(within(form()).getByDisplayValue('https://api.example.com/v1')).toBeTruthy())

    fireEvent.change(within(form()).getByDisplayValue('https://api.example.com/v1'), {
      target: { value: 'ftp://example.com' },
    })
    fireEvent.click(within(form()).getByRole('button', { name: '查询模型' }))

    expect(await screen.findByText('请先填写合法的 API 地址')).toBeTruthy()
    expect(client.blog.listTranslateModels).not.toHaveBeenCalled()
  })

  it('翻译配置加载失败：显示失败状态，编辑后也不能保存', async () => {
    client.blog.getTranslateConfig.mockRejectedValue(new Error('down'))
    const { container } = renderSettings()
    await waitFor(() => expect(within(translateForm(container)).getByText('加载失败')).toBeTruthy())
    expect(within(translateForm(container)).getByText(/翻译配置加载失败/)).toBeTruthy()

    // 改动一个字段让 translateDirty 为真，但表单里的其余值仍只是默认值：
    // 保存会把它们当成「用户想要的值」写回，覆盖服务端真实的配置。
    fireEvent.click(within(translateForm(container)).getByRole('checkbox'))
    expect(saveTranslateButton(container).disabled).toBe(true)

    // Enter 仍能提交表单，守卫必须拦下并提示刷新。
    fireEvent.submit(translateForm(container))
    await waitFor(() =>
      expect(screen.getAllByText('翻译配置尚未加载成功，请刷新页面后重试').length).toBeGreaterThan(0),
    )
    expect(client.blog.updateTranslateConfig).not.toHaveBeenCalled()
  })

  it('旧版服务端没有模板字段：界面按空模板工作；地址留空时查询只带密钥', async () => {
    // 不带 prompt_template 的响应模拟升级前的服务端：?? 右侧的空串必须兜住。
    const legacy = {
      enabled: true,
      url: '',
      model: 'gpt-x',
      timeout_seconds: 60,
      max_tokens: 4096,
      api_key_configured: true,
      configured: true,
    }
    client.blog.getTranslateConfig.mockResolvedValue(legacy)
    client.blog.listTranslateModels.mockResolvedValue({ models: ['m1'] })
    client.blog.updateTranslateConfig.mockResolvedValue(legacy)
    const { container } = renderSettings()
    const form = () => translateForm(container)
    const prompt = () => translateForm(container).querySelector('textarea') as HTMLTextAreaElement
    const keyInput = () => within(form()).getByPlaceholderText('已保存，留空保持不变') as HTMLInputElement
    await waitFor(() => expect(within(form()).getByDisplayValue('gpt-x')).toBeTruthy())
    expect(prompt().value).toBe('')

    // URL 留空是合法状态：查询沿用服务端已保存的地址，只把新填的密钥带上。
    fireEvent.change(keyInput(), { target: { value: ' sk-query ' } })
    fireEvent.click(within(form()).getByRole('button', { name: '查询模型' }))
    await waitFor(() => expect(client.blog.listTranslateModels).toHaveBeenCalledWith({ api_key: 'sk-query' }))
    expect(await screen.findByRole('button', { name: 'm1' })).toBeTruthy()

    // 密钥也留空 → 整个请求体为空，两端都采用已保存值。
    fireEvent.change(keyInput(), { target: { value: '   ' } })
    fireEvent.click(within(form()).getByRole('button', { name: '查询模型' }))
    await waitFor(() => expect(client.blog.listTranslateModels).toHaveBeenLastCalledWith({}))

    // 改密钥后保存：响应仍没有模板字段，回填后模板保持空串。
    fireEvent.change(keyInput(), { target: { value: 'sk-save' } })
    fireEvent.click(saveTranslateButton(container))
    await waitFor(() =>
      expect(client.blog.updateTranslateConfig).toHaveBeenCalledWith(expect.objectContaining({ api_key: 'sk-save' })),
    )
    await waitFor(() => expect(prompt().value).toBe(''))
  })

  it('思维强度：七个预设档位可选，回显、保存与清空', async () => {
    client.blog.getTranslateConfig.mockResolvedValue(translateConfig({ thinking_effort: 'low' }))
    client.blog.updateTranslateConfig.mockResolvedValue(translateConfig({ thinking_effort: 'xhigh' }))
    const { container } = renderSettings()
    const form = () => translateForm(container)
    // 先等配置加载完（加载态下整页只有 Loading，没有表单）。
    const combo = await screen.findByRole('combobox', { name: '思维强度' })
    expect(within(form()).getByText('低')).toBeTruthy()

    fireEvent.mouseDown(combo)
    // minimal / low / medium / high / xhigh / max / ultra 全部在候选里。
    for (const name of ['极低', '低', '中', '高', '超高', '最高', '极限']) {
      expect(screen.getByRole('option', { name })).toBeTruthy()
    }
    fireEvent.click(screen.getByRole('option', { name: '超高' }))
    expect(screen.getAllByText('有未保存的修改').length).toBeGreaterThan(0)

    fireEvent.click(saveTranslateButton(container))
    await waitFor(() =>
      expect(client.blog.updateTranslateConfig).toHaveBeenCalledWith(
        expect.objectContaining({ thinking_effort: 'xhigh' }),
      ),
    )
    await waitFor(() => expect(within(form()).getByText('超高')).toBeTruthy())

    // 清空 = 不发送 reasoning_effort（以 placeholder 呈现，不撑高触发器）。
    client.blog.updateTranslateConfig.mockResolvedValue(translateConfig({ thinking_effort: '' }))
    fireEvent.click(within(form()).getByRole('button', { name: '清空' }))
    const placeholder = within(form()).getByText('不设置')
    expect(placeholder.className).toContain('select-placeholder')
    fireEvent.click(saveTranslateButton(container))
    await waitFor(() =>
      expect(client.blog.updateTranslateConfig).toHaveBeenLastCalledWith(
        expect.objectContaining({ thinking_effort: '' }),
      ),
    )
  })

  it('AI 配置非法时直接提交表单被拦截，不发请求', async () => {
    client.blog.getTranslateConfig.mockResolvedValue(translateConfig())
    const { container } = renderSettings()
    await screen.findByRole('combobox', { name: '思维强度' })
    const form = translateForm(container)
    fireEvent.change(within(form).getByDisplayValue('https://api.example.com/v1'), {
      target: { value: 'ftp://bad' },
    })

    // Enter / form.submit 能绕过 disabled 的按钮：防御分支必须自己拦住。
    fireEvent.submit(form)
    expect(await screen.findByText('请先修正 AI 翻译配置中的错误')).toBeTruthy()
    expect(client.blog.updateTranslateConfig).not.toHaveBeenCalled()
  })

  it('AI 配置保存进行中重复提交只发一次请求', async () => {
    client.blog.getTranslateConfig.mockResolvedValue(translateConfig())
    let resolveSave: (value: ReturnType<typeof translateConfig>) => void = () => {}
    client.blog.updateTranslateConfig.mockReturnValueOnce(new Promise((resolve) => { resolveSave = resolve }))
    const { container } = renderSettings()
    await screen.findByRole('combobox', { name: '思维强度' })
    const form = translateForm(container)
    fireEvent.change(within(form).getByDisplayValue('gpt-test'), { target: { value: 'gpt-2' } })
    fireEvent.submit(form)
    await waitFor(() => expect(client.blog.updateTranslateConfig).toHaveBeenCalledTimes(1))

    fireEvent.submit(form)
    expect(client.blog.updateTranslateConfig).toHaveBeenCalledTimes(1)

    await act(async () => { resolveSave(translateConfig({ model: 'gpt-2' })) })
  })
})
