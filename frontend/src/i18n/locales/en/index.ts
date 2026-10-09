import landing from './landing'
import common from './common'
import dashboard from './dashboard'
import channelMonitorV2 from './channelMonitorV2'
import batchImage from './batchImage'
import admin from './admin'
import misc from './misc'

export default {
  imageGeneration: {
    "softRatio": "Aspect ratio is a composition preference; the model determines the actual output dimensions.",
    "exactCanvas": "Selected size is returned as the final canvas (gateway local pad/coerce).",
    "prompt": "Image prompt",
    "verifyKey": "Verify key",
    "verifyHint": "This checks the key only. Image generation is unverified. Generate in Studio; usage is billed.",
    "keyValid": "Key valid",
    "keyVerified": "Key valid · Image generation unverified",
    "openStudio": "Open in Studio"
},
  videoUnderstanding: {
    hint: 'kimi-k3 video understanding: prefer /v1/chat/completions + video_url (base64 is the stable path; HTTPS only if the serving supply can fetch it). /v1/responses + input_video also works. Offshore public URLs are often rejected. This is not “Moonshot requires Responses”. Key verification still sends a text ping only.',
  },
  ...landing,
  ...common,
  ...dashboard,
  ...channelMonitorV2,
  ...batchImage,
  admin,
  ...misc,
}
