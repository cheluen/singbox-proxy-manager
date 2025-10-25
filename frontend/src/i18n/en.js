export default {
  // Common
  app_title: 'Proxy Forwarding Management',
  version: 'Version',
  confirm: 'Confirm',
  cancel: 'Cancel',
  save: 'Save',
  delete: 'Delete',
  edit: 'Edit',
  add: 'Add',
  search: 'Search',
  refresh: 'Refresh',
  loading: 'Loading...',
  success: 'Success',
  error: 'Error',
  warning: 'Warning',
  
  // Login
  login_title: 'Admin Login',
  password: 'Password',
  login: 'Login',
  logout: 'Logout',
  login_success: 'Login successful',
  login_failed: 'Login failed',
  
  // Navigation
  nodes: 'Node Management',
  settings: 'Settings',
  
  // Node Management
  node_list: 'Node List',
  add_node: 'Add Node',
  import_node: 'Import Node',
  batch_import: 'Batch Import',
  batch_delete: 'Batch Delete',
  batch_check_ip: 'Batch Check IP',
  set_auth: 'Set Auth',
  node_name: 'Node Name',
  node_type: 'Type',
  inbound_port: 'Inbound Port',
  username: 'Username',
  password_auth: 'Password',
  enabled: 'Enabled',
  disabled: 'Disabled',
  node_ip: 'Node IP',
  location: 'Location',
  latency: 'Latency',
  actions: 'Actions',
  auto_assign: 'Auto Assign',
  
  // Node Form
  node_config: 'Node Config',
  share_link: 'Share Link',
  parse_link: 'Parse Link',
  config_json: 'Config JSON',
  
  // Batch Operations
  batch_import_title: 'Batch Import Nodes',
  batch_import_desc: 'One share link per line. Supports SS, VLESS, VMess, Hysteria2, TUIC',
  paste_links: 'Paste Links',
  import_success: 'Successfully imported {{count}} nodes',
  import_failed: 'Failed to import {{count}} nodes',
  enable_after_import: 'Enable nodes after import',
  
  batch_auth_title: 'Batch Set Authentication',
  batch_auth_desc: 'Set username and password for selected nodes',
  apply_auth: 'Apply Auth',
  
  batch_delete_confirm: 'Confirm delete {{count}} selected nodes?',
  batch_delete_success: 'Successfully deleted {{count}} nodes',
  
  batch_check_ip_running: 'Checking {{current}}/{{total}} nodes...',
  batch_check_ip_success: 'Completed IP check for {{count}} nodes',
  
  // Settings
  admin_password: 'Admin Password',
  start_port: 'Start Port',
  update_password: 'Update Password',
  new_password: 'New Password',
  confirm_password: 'Confirm Password',
  password_mismatch: 'Passwords do not match',
  settings_updated: 'Settings updated',
  
  // Messages
  node_created: 'Node created',
  node_updated: 'Node updated',
  node_deleted: 'Node deleted',
  nodes_reordered: 'Nodes reordered',
  ip_check_started: 'IP check started',
  auth_updated: 'Authentication updated',
  
  // Errors
  network_error: 'Network error',
  server_error: 'Server error',
  invalid_request: 'Invalid request',
  unauthorized: 'Unauthorized, please login again',
  node_not_found: 'Node not found',
  invalid_config: 'Invalid configuration',
  invalid_link: 'Invalid share link',
  
  // Placeholders
  enter_password: 'Please enter password',
  enter_node_name: 'Please enter node name',
  enter_share_link: 'Please enter share link',
  enter_username: 'Please enter username',
  select_nodes: 'Please select nodes',
  no_nodes: 'No nodes yet',
  
  // Table
  select_all: 'Select All',
  selected_count: '{{count}} selected',
  total_count: 'Total {{count}}'
}
