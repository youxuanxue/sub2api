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
    "prompt": "生图提示词",
    "verifyKey": "验证密钥",
    "verifyHint": "此处只验证密钥，不验证生图。请在 Studio 中生成图片，生成会产生费用。",
    "keyValid": "密钥有效",
    "keyVerified": "密钥有效 · 生图未验证",
    "openStudio": "在 Studio 中打开"
},
  ...landing,
  ...common,
  ...dashboard,
  ...channelMonitorV2,
  ...batchImage,
  admin,
  ...misc,
}
