import { createApp } from 'vue'
import { createPinia } from 'pinia'
import ElementPlus from 'element-plus'
import 'element-plus/dist/index.css'
import 'element-plus/theme-chalk/dark/css-vars.css'

import App from './App.vue'
import router from './router'
import './style.css'

// 尽早设置标题以防止闪烁
const savedTitle = localStorage.getItem('siteTitle')
if (savedTitle) {
    document.title = savedTitle
}

const app = createApp(App)

// 使用插件
app.use(createPinia())
app.use(router)
app.use(ElementPlus)

app.mount('#app')
