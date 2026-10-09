import landing from './landing'
import common from './common'
import dashboard from './dashboard'
import channelMonitorV2 from './channelMonitorV2'
import batchImage from './batchImage'
import admin from './admin'
import misc from './misc'

export default {
  imageGeneration: {
    "softRatio": "比例为构图偏好，实际输出尺寸由模型决定。",
    "exactCanvas": "所选尺寸由网关本地精确画布兑现（上游像素可能先漂移，再 pad/coerce）。",
    "prompt": "生图提示词",
    "verifyKey": "验证密钥",
    "verifyHint": "此处只验证密钥，不验证生图。请在 Studio 中生成图片，生成会产生费用。",
    "keyValid": "密钥有效",
    "keyVerified": "密钥有效 · 生图未验证",
    "openStudio": "在 Studio 中打开"
},
  videoUnderstanding: {
    hint: 'kimi-k3 视频理解：优先 /v1/chat/completions + video_url（base64 最稳，或供应侧能拉取的 HTTPS）。/v1/responses + input_video 同样可用。境外公网 URL 常被拒绝；这不是「官方必须走 Responses」。密钥自检仍只发文本 ping。',
  },
  ...landing,
  ...common,
  ...dashboard,
  ...channelMonitorV2,
  ...batchImage,
  admin,
  ...misc,
}
