export default {
  // Common
  app_title: '代理转发管理系统',
  version: '版本',
  confirm: '确认',
  cancel: '取消',
  save: '保存',
  delete: '删除',
  edit: '编辑',
  add: '添加',
  search: '搜索',
  refresh: '刷新',
  loading: '加载中...',
  success: '成功',
  error: '错误',
  warning: '警告',
  
  // Login
  login_title: '管理员登录',
  password: '密码',
  login: '登录',
  logout: '退出登录',
  login_success: '登录成功',
  login_failed: '登录失败',
  
  // Navigation
  nodes: '节点管理',
  settings: '系统设置',
  
  // Node Management
  node_list: '节点列表',
  add_node: '添加节点',
  import_node: '导入节点',
  batch_import: '批量导入',
  batch_delete: '批量删除',
  batch_check_ip: '批量测IP',
  set_auth: '批量认证',
  node_name: '节点名称',
  node_type: '节点类型',
  inbound_port: '入站端口',
  username: '用户名',
  password_auth: '认证密码',
  enabled: '启用',
  disabled: '禁用',
  node_ip: '节点IP',
  location: '位置',
  latency: '延迟',
  actions: '操作',
  auto_assign: '自动分配',
  
  // Node Form
  node_config: '节点配置',
  share_link: '分享链接',
  parse_link: '解析链接',
  config_json: '配置JSON',
  
  // Batch Operations
  batch_import_title: '批量导入节点',
  batch_import_desc: '每行一个分享链接，支持 SS、VLESS、VMess、Hysteria2、TUIC',
  paste_links: '粘贴链接',
  import_success: '成功导入 {{count}} 个节点',
  import_failed: '{{count}} 个节点导入失败',
  enable_after_import: '导入后启用节点',
  
  batch_auth_title: '批量设置认证',
  batch_auth_desc: '为选中的节点统一设置用户名和密码',
  apply_auth: '应用认证',
  
  batch_delete_confirm: '确认删除选中的 {{count}} 个节点？',
  batch_delete_success: '成功删除 {{count}} 个节点',
  
  batch_check_ip_running: '正在检测 {{current}}/{{total}} 个节点...',
  batch_check_ip_success: '完成 {{count}} 个节点IP检测',
  
  // Settings
  admin_password: '管理员密码',
  start_port: '起始端口',
  update_password: '更新密码',
  new_password: '新密码',
  confirm_password: '确认密码',
  password_mismatch: '两次输入的密码不一致',
  settings_updated: '设置已更新',
  
  // Messages
  node_created: '节点已创建',
  node_updated: '节点已更新',
  node_deleted: '节点已删除',
  nodes_reordered: '节点顺序已更新',
  ip_check_started: 'IP检测已开始',
  auth_updated: '认证设置已更新',
  
  // Errors
  network_error: '网络错误',
  server_error: '服务器错误',
  invalid_request: '无效的请求',
  unauthorized: '未授权，请重新登录',
  node_not_found: '节点不找到',
  invalid_config: '无效的配置',
  invalid_link: '无效的分享链接',
  
  // Placeholders
  enter_password: '请输入密码',
  enter_node_name: '请输入节点名称',
  enter_share_link: '请输入分享链接',
  enter_username: '请输入用户名',
  select_nodes: '请选择节点',
  no_nodes: '暂无节点',
  
  // Table
  select_all: '全选',
  selected_count: '已选 {{count}} 项',
  total_count: '共 {{count}} 项'
}
