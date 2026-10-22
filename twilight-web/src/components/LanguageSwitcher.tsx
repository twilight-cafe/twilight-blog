// LanguageSwitcher.tsx —— 全站语言切换器
//
// 形态是两枚紧挨的小按钮（中 / EN），选中态实底。它出现在四个地方：
//   - 首页大标题右侧与内页精简头（替换原来那行拉丁文副标题）；
//   - 后台顶栏；
//   - 登录 / 注册 / 找回密码 / 安装页（绝对定位在右上角）。
//
// 为什么不是下拉菜单：只有两种语言，下拉要把「当前语言」藏进一次点击之后，
// 而这里的目标恰恰是让读者一眼看见「还能看英文」。两个按钮的总宽度也远小于
// 一个带箭头的 Select。
//
// 可访问性：外层是 role="group" + aria-label，按钮用 aria-pressed 表达选中态
// （而不是只靠 CSS 的 .on 类），键盘 Tab 能逐个到达，回车/空格即切换。

import { useI18n, type Locale } from '../lib/i18n'

export interface LanguageSwitcherProps {
  /** 额外类名。登录页用 `auth-lang` 做绝对定位，其余场景不传。 */
  className?: string
}

interface LocaleOption {
  locale: Locale
  /** 按钮上显示的字面量：保持极短，两个加起来不抢标题的视觉重量。 */
  label: string
  /** title / 读屏用的完整语言名。 */
  name: string
}

const OPTIONS: readonly LocaleOption[] = [
  { locale: 'zh', label: '中', name: '中文' },
  { locale: 'en', label: 'EN', name: 'English' },
]

export default function LanguageSwitcher({ className }: LanguageSwitcherProps) {
  const { locale, setLocale, t } = useI18n()

  // 拼接而不是模板字符串里判空：className 为空时不能留下一个尾随空格，
  // 否则 `.lang-switch ` 这种选择器断言（测试里按 class 精确比对）会失败。
  const classes = className ? `lang-switch ${className}` : 'lang-switch'

  return (
    <div className={classes} role="group" aria-label={t('语言', 'Language')}>
      {OPTIONS.map((option) => {
        const active = option.locale === locale
        return (
          <button
            key={option.locale}
            type="button"
            // `on` 是站点样式的选中态钩子；aria-pressed 才是读屏的判据。
            className={active ? 'on' : undefined}
            aria-pressed={active}
            title={option.name}
            lang={option.locale === 'zh' ? 'zh-CN' : 'en'}
            onClick={() => setLocale(option.locale)}
          >
            {option.label}
          </button>
        )
      })}
    </div>
  )
}
