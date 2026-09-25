// email-code-cooldown.ts —— 验证码发送的 60 秒冷却（前端侧）
//
// 为什么要有它：服务端对同一收件地址强制了 60 秒最短间隔（见
// service.emailCodeMinInterval），但**服务端只能拒绝，不能替用户数秒**。
// 没有倒计时的话，用户点「重新发送」会立刻拿到一个 429 和一句「请 60 秒后再试」，
// 却不显示还要等多久——他唯一能做的就是反复点，继续产生请求。
//
// 三层防护的分工（都要有，缺一层就有缺口）：
//   1. 本 hook：按钮倒计时禁用。体验层，直接消除无害的重复点击；
//   2. 服务端 60 秒间隔：权威判定。绕过前端直接打接口时，唯一还拦得住的；
//   3. 服务端按地址每小时的额度：控制「一个收件箱总共被打扰多少次」。
//
// ⚠️ 这一层**不是**安全边界。它跑在用户自己的浏览器里，改个变量就没了。
// 任何「前后端一起限制」的需求，前端的部分都只是体验优化——真正的闸门必须在
// 服务端，否则就是一个看起来有防护、实际可绕过的假象。
//
// 为什么做在模块级而不是组件内 useState：Register 与 ForgotPassword 是两个独立
// 组件，各持一份状态的话，用户在注册页发过码、跳到忘记密码页再发，就会得到
// 一个「倒计时说可以发」而服务端拒绝的矛盾状态。做成模块级后，只要没刷新页面，
// 两个页面共享同一条时间线——与服务端的判定口径一致。
//
// 刷新页面会丢失倒计时，这是可接受的：刷新后连点，服务端仍然是权威的那道闸。

import { useEffect, useState } from 'react'
import { isBlogApiError } from 'twilight-blog-api'

/** 冷却时长（秒）。与后端 service.emailCodeMinInterval 镜像；改一处必须改另一处。 */
export const EMAIL_CODE_COOLDOWN_SECONDS = 60

// 模块级：记录每个邮箱地址何时可以被再次请求（epoch 毫秒）。
// 按地址分桶而不是全局一个值：用户改了邮箱就该能立刻给新地址发。
const availableAt = new Map<string, number>()
const listeners = new Set<() => void>()

/**
 * 把地址归一化后再做键。与服务端 normalizeEmail 同口径（小写 + 去空白），
 * 否则 "A@x.com" 与 "a@x.com" 会被前端当成两个地址、各给一份冷却，
 * 而服务端把它们当同一个收件箱——前端就会显示「可以发送」但实际被拒。
 */
function keyOf(email: string): string {
  return email.trim().toLowerCase()
}

function remainingSeconds(email: string, now: number): number {
  const until = availableAt.get(keyOf(email))
  if (until === undefined) return 0
  const left = until - now
  return left <= 0 ? 0 : Math.ceil(left / 1000)
}

function notify() {
  for (const listener of listeners) listener()
}

/**
 * 启动（或重启用）某个地址的冷却。
 *
 * 由调用方在**服务端确认发送成功之后**调用，而不是在点击时。理由：点击时启动
 * 会让一次「邮箱格式错」或「服务端拒发」也消耗掉 60 秒——用户改完错字回来，
 * 倒计时还在跑，而服务端其实根本没发。以服务端的结果为准，前端的倒计时才和
 * 服务端的记录对得上。
 *
 * 服务端返回 429（间隔不足）时也调用它，把按钮锁住——这正是我们要消除的
 * 「反复点、反复被拒」循环。
 */
export function startEmailCodeCooldown(email: string, seconds: number = EMAIL_CODE_COOLDOWN_SECONDS) {
  if (seconds <= 0) return
  availableAt.set(keyOf(email), Date.now() + seconds * 1000)
  notify()
}

/** 清掉某个地址（或全部）的冷却。测试用，也可用于服务端退回更短的时间。 */
export function clearEmailCodeCooldown(email?: string) {
  if (email === undefined) availableAt.clear()
  else availableAt.delete(keyOf(email))
  notify()
}

/**
 * 判断一个错误是不是「被限流」（HTTP 429）。
 *
 * 为什么不能只看文案：服务端有两条 429——「这个地址今天要得太多」
 * （ErrEmailCodeAddressLimit）与「离上一封不到 60 秒」（ErrEmailCodeTooFrequent）。
 * 文案可能随时调整，靠 includes('频繁') 之类的匹配迟早会失效，而且失效时是静默的
 * （只是倒计时不启动，看起来像正常行为）。
 *
 * 用 SDK 自带的 isBlogApiError 收窄而不是自己 duck-type：后者接受任何带 status
 * 的 object，一个恰好有 status 字段的业务错误就会被误判成限流。
 *
 * status 取不到（网络层错误）时返回 false——宁可少锁一次，也不要因为一次断网
 * 把按钮锁住 60 秒却什么都没发出去。
 */
export function isRateLimited(error: unknown): boolean {
  return isBlogApiError(error) && error.status === 429
}

/**
 * 订阅某个地址的剩余冷却秒数（0 表示可以发送）。
 *
 * 用 useState + 订阅而不是 useSyncExternalStore：后者要求 getSnapshot 在
 * 同一时刻返回同一个值，而这里的时间戳每秒都在动，写起来要额外做缓存层，
 * 收益不抵复杂度。这里的组件很少（两个登录页），重渲染代价可以忽略。
 *
 * 只在真正需要倒计时的时候才起定时器：remaining === 0 时直接不挂 interval。
 * 一个每秒都跑的定时器即使什么都不做，也会让每个登录页常驻一个唤醒源。
 */
export function useEmailCodeCooldown(email: string): number {
  const [remaining, setRemaining] = useState(() => remainingSeconds(email, Date.now()))

  useEffect(() => {
    const sync = () => setRemaining(remainingSeconds(email, Date.now()))
    // 换地址时先同步一次：否则会显示上一个地址剩下的秒数。
    sync()

    listeners.add(sync)
    return () => {
      listeners.delete(sync)
    }
  }, [email])

  useEffect(() => {
    if (remaining <= 0) return
    const timer = window.setInterval(() => {
      setRemaining(remainingSeconds(email, Date.now()))
    }, 1000)
    return () => window.clearInterval(timer)
  }, [email, remaining])

  return remaining
}
