// 浏览器端的 node:fs/promises 替身。
//
// twilight-blog-api 的媒体上传支持两种入参：浏览器里传 File/Blob，
// 或在 Node 下传一个磁盘路径。后者靠动态 import('node:fs/promises') 实现，
// 在浏览器里那条分支永远不会被执行——但打包器仍然要为这个 specifier
// 找到一个能解析的模块，否则整个构建直接失败。
//
// 于是 twilight-web/vite.config.ts 把 node:fs/promises alias 到本文件。
// 真的被调用到，就说明有人从浏览器传了路径；此时抛一个讲清原因的异常，
// 比返回 undefined 之后再在别处莫名其妙地崩掉要好。
//
// 注意 alias 只作用于打包：tsconfig.app.json 的 paths 里没有这一项，
// 所以类型检查看到的仍是 @types/node 提供的真实签名。
export async function readFile(): Promise<never> {
  throw new Error('twilight-blog-api path-based media upload is not supported in the browser; pass a File/Blob instead.')
}
