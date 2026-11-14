// 新面板新增的中文词条。线上面板已有的词条在 base-zh.js 中原样保留，这里只放新增部分。
export default {
  // 通用
  all: '全部',
  any: '不限',
  clear: '清空',
  close: '关闭',
  show: '显示',
  hide: '隐藏',
  retry: '重试',
  language: '语言',
  configure: '配置',
  configured: '已配置',
  configured_unsaved: '已修改，保存设置后生效',
  not_configured: '未配置',
  more_actions: '更多操作',
  unsaved_changes: '有未保存的修改',
  updated_at: '更新时间',

  // 登录
  login_wrong_secret: '密码不正确，请重试',

  // 运行状态
  runtime_running: '内核运行中',
  runtime_unknown: '状态未知',

  // 列表与概览
  status_unverified_tip: '还没有检测过，或上一次检测失败',
  region: '地区',
  status_unverified_short: '未验证',
  count_nodes: '{{count}} 个节点',
  region_unknown: '未知',
  direct_local: '本机直连',
  server_address: '服务器地址',
  server_port: '服务器端口',
  node_enabled_label: '启用节点',
  select_node: '选择 {{name}}',
  selected_suffix: '项已选',
  clear_selection: '取消选择',
  bulk_actions: '批量操作',
  loading_more: '已显示 {{shown}} / {{total}}，继续滚动加载更多',
  empty_title: '还没有节点',
  empty_hint: '手动添加一个节点，或者粘贴分享链接、订阅内容批量导入。',
  empty_filtered_title: '没有匹配的节点',
  empty_filtered_hint: '没有节点符合当前的搜索或筛选条件。',

  // 搜索与筛选
  search_placeholder: '搜索名称、备注、IP、地区、端口…',
  filters: '筛选',
  remove_filter: '移除筛选',
  filter_result_count: '显示 {{shown}} / {{total}}',
  filter_ports_invalid: '格式不正确，示例：30001-30005, 30008',

  // 排序
  drag_sort_hint_short: '拖动排序（键盘：空格抓起，方向键移动）',
  move_top: '移到最前',
  move_up: '上移',
  move_down: '下移',
  move_bottom: '移到最后',

  // 测 IP
  check_all_visible: '全部测 IP',
  checking: '检测中',
  check_done: 'IP 检测完成',
  check_failed: 'IP 检测失败',
  check_failed_count: '{{count}} 个失败',
  check_summary_mixed: '检测完成：{{ok}} 个可用，{{failed}} 个失败',
  check_none_enabled: '没有可检测的节点（已禁用的节点无法检测）',
  check_skipped_disabled: '已跳过 {{count}} 个已禁用的节点',
  check_disabled_hint: '节点已禁用，启用后才能检测出口 IP。',

  // 节点详情
  section_connect: '连接方式',
  section_exit: '出口信息',
  section_inbound: '本地入站',
  section_outbound: '出站',
  auth_secret: '认证密码',
  auth_none: '无认证',
  auth_open_warning: '入站未启用认证，任何能访问该端口的人都可以使用这个代理。',
  connect_host_hint: '地址中的主机取自你当前访问面板所用的地址（{{host}}）',
  connect_no_port: '尚未分配入站端口',
  connect_disabled_hint: '节点已禁用，下面的地址暂时无法连接。',
  copy_proxy_http: '复制 HTTP 代理地址',
  copy_proxy_socks: '复制 SOCKS5 代理地址',
  tcp_reuse_explain: '在任意一个启用了认证的入站端口上，使用这个用户名和本节点的密码登录，流量就会改走本节点。',
  remark_empty_hint: '添加备注，例如到期时间、用途',
  remark_saved: '备注已保存',

  // 新增 / 编辑节点
  add_manual: '手动填写',
  add_from_link: '粘贴分享链接',
  more_add_options: '更多添加方式',
  create_node: '创建节点',
  edit_node: '编辑节点',
  link_import_title: '有分享链接？粘贴后自动填好下面的表单',
  link_import_placeholder: '粘贴分享链接，回车解析',
  link_import_hint: '支持 ss、vless、vmess、trojan、hysteria2、tuic、anytls、socks5、http 等链接。',
  link_import_filled: '已根据链接填好 {{type}} 配置，确认无误后保存即可。保存后会自动检测一次出口 IP。',
  form_group_basic: '基本信息',
  form_group_protocol: '协议参数',
  form_group_transport: '传输层',
  form_group_tls: 'TLS',
  form_group_advanced: '高级选项',
  form_group_inbound: '本地入站',
  direct_hint: '直连节点不经过远端服务器，流量从本机直接发出（可配合前置代理使用）。',
  port_hint_preserve: '留空或填 0 表示自动分配一个可用端口。',
  port_hint_pinned: '该端口已固定，可以手动修改，不参与自动重排。',
  port_hint_managed: '由系统按节点顺序分配。想手动指定：固定该节点的端口，或在系统设置里开启「保留入站端口」。',
  auth_generate: '随机生成',
  auth_on_hint: '客户端需要提供用户名和密码才能使用该入站。',
  auth_off_hint: '已关闭：任何能访问该端口的人都可以使用这个代理。',
  raw_config_title: '查看当前存储的完整配置',
  raw_config_hint: '只读。表单没有覆盖到的字段在保存时会原样保留。',
  delete_node_title: '删除节点「{{name}}」？',
  delete_node_desc: '删除后无法恢复，对应的入站端口会立即停止服务。',
  replace_with_link: '用分享链接替换',

  // 前置代理
  upstream_mode_hint_none: '不经过任何前置代理，直接连接',
  upstream_mode_hint_global: '使用系统设置里的全局前置代理',
  upstream_mode_hint_custom: '只给这个节点单独指定一个前置代理',
  upstream_mode_hint_legacy: '沿用旧配置里的 detour',
  upstream_global_enable: '启用全局前置代理',
  upstream_global_off_hint: '全局前置代理当前未启用。在系统设置中启用之前，该节点仍为直连。',

  // 导入导出
  export_share_link: '导出分享链接',
  export_desc: '共 {{count}} 条分享链接，每行一条。',
  download_txt: '下载 .txt',
  import_action: '导入',
  import_empty: '请先粘贴要导入的内容',
  import_line_count: '{{count}} 行',
  import_placeholder: '每行一条分享链接，也可以粘贴 Base64 订阅内容、Clash Meta YAML 或订阅地址\n\nvless://…\nss://…\nhttps://example.com/sub',
  import_result: '成功 {{success}} 个，失败 {{failed}} 个',
  import_retry_blocked: '成功的部分已经导入。请删掉已成功的行或修改内容后再提交，避免重复导入。',
  import_source_auto: '自动识别',
  import_source_subscription: '订阅地址',
  import_source_http_proxy: 'HTTP 代理节点',
  import_source_auto_hint: 'http(s):// 开头的行会被当作订阅地址去拉取。',
  import_source_subscription_hint: '内容按订阅地址处理；订阅请求失败不会降级为代理节点。',
  import_source_http_proxy_hint: 'http(s):// 开头的行会被当作 HTTP/HTTPS 代理节点导入，而不是订阅。',

  // 批量认证
  batch_auth_scope: '将应用到已选中的 {{count}} 个节点。',
  batch_auth_on_hint: '所选节点将统一使用下面的用户名和密码。',
  batch_auth_disable_action: '关闭认证',

  // 设置
  settings_ports: '端口分配',
  confirm_new_secret: '再次输入新密码',
  relogin_notice: '修改密码后需要使用新密码重新登录。',

  // 命令面板与快捷键（暗色指挥台）
  cmd_open: '搜索节点或执行操作',
  cmd_placeholder: '输入节点名称，或输入操作名…',
  cmd_actions: '操作',
  cmd_empty: '没有匹配的节点或操作',
  cmd_selected: '{{action}}（已选 {{count}} 个）',
  key_move: '移动',
  key_expand: '展开',
  key_select: '选择',

  // 预览站点
}
