// 粒子系统（A18）：对象池预分配 + 空闲链表，零 GC 压力。
const MAX = 500;

export class Particles {
  constructor() {
    this.pool = [];
    this.active = [];
    for (let i = 0; i < MAX; i++) {
      this.pool.push({
        x: 0, y: 0, vx: 0, vy: 0, life: 0, maxLife: 1,
        size: 3, color: '#fff', rot: 0, vr: 0, shape: 'rect',
      });
    }
    this.enabled = true;
    this.density = 1; // 画质降级：1 / 0.5 / 0
  }

  spawn(x, y, opts = {}) {
    const count = Math.round((opts.count || 28) * this.density * (this.enabled ? 1 : 0));
    const colors = opts.colors || ['#d4a941', '#a9793f', '#f7e7c8'];
    for (let i = 0; i < count; i++) {
      const p = this.pool.pop();
      if (!p) return;
      const angle = Math.random() * Math.PI * 2;
      const speed = 80 + Math.random() * (opts.speed || 160);
      p.x = x; p.y = y;
      p.vx = Math.cos(angle) * speed;
      p.vy = Math.sin(angle) * speed - (opts.lift || 60);
      p.maxLife = 0.4 + Math.random() * 0.4;
      p.life = p.maxLife;
      p.size = 2 + Math.random() * (opts.size || 3.5);
      p.color = colors[(Math.random() * colors.length) | 0];
      p.rot = Math.random() * Math.PI * 2;
      p.vr = (Math.random() - 0.5) * 10;
      p.shape = Math.random() < 0.6 ? 'rect' : 'circle';
      this.active.push(p);
    }
  }

  confetti(w, h) {
    const count = Math.round(80 * this.density * (this.enabled ? 1 : 0));
    const colors = ['#d4a941', '#e05a3a', '#f7e7c8', '#7ec97e', '#6fa8d5'];
    for (let i = 0; i < count; i++) {
      const p = this.pool.pop();
      if (!p) return;
      p.x = Math.random() * w;
      p.y = -10 - Math.random() * h * 0.3;
      p.vx = (Math.random() - 0.5) * 40;
      p.vy = 60 + Math.random() * 90;
      p.maxLife = 2 + Math.random() * 1.5;
      p.life = p.maxLife;
      p.size = 3 + Math.random() * 4;
      p.color = colors[(Math.random() * colors.length) | 0];
      p.rot = Math.random() * Math.PI * 2;
      p.vr = (Math.random() - 0.5) * 8;
      p.shape = 'rect';
      this.active.push(p);
    }
  }

  update(dt) {
    for (let i = this.active.length - 1; i >= 0; i--) {
      const p = this.active[i];
      p.life -= dt;
      if (p.life <= 0) {
        this.active.splice(i, 1);
        this.pool.push(p);
        continue;
      }
      p.x += p.vx * dt;
      p.y += p.vy * dt;
      p.vy += 600 * dt; // 重力
      p.rot += p.vr * dt;
    }
  }

  draw(ctx) {
    for (const p of this.active) {
      const alpha = Math.min(1, p.life / p.maxLife * 1.6);
      ctx.save();
      ctx.globalAlpha = alpha;
      ctx.fillStyle = p.color;
      ctx.translate(p.x, p.y);
      ctx.rotate(p.rot);
      if (p.shape === 'rect') {
        ctx.fillRect(-p.size / 2, -p.size / 4, p.size, p.size / 2);
      } else {
        ctx.beginPath();
        ctx.arc(0, 0, p.size / 2, 0, Math.PI * 2);
        ctx.fill();
      }
      ctx.restore();
    }
  }

  get count() { return this.active.length; }
}
