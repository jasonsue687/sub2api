import anthropicRequests from './anthropicRequests'
import anthropicOutbound from './anthropicOutbound'
import overview from './overview'
import channels from './channels'
import accounts from './accounts'
import resources from './resources'
import ops from './ops'
import settings from './settings'
import audit from './audit'
import promptAudit from './promptAudit'
import plugins from './plugins'

export default {
  ...anthropicRequests,
  ...anthropicOutbound,
  ...overview,
  ...channels,
  ...accounts,
  ...resources,
  ...ops,
  ...settings,
  ...audit,
  ...promptAudit,
  ...plugins,
}
