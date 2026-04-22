FROM node:22-alpine

WORKDIR /app

COPY package*.json ./

RUN npm install

COPY . .

EXPOSE 5173

# Start Vite in development mode
# "npm run dev" -> runs "vite" (from package.json scripts)
# if it's not started in dev mode, it wouldn't be possible to proxy the requests with vite 
CMD ["npm", "run", "dev"]